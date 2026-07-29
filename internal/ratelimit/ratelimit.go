// Package ratelimit bounds how often a credential can be guessed.
//
// It counts FAILURES, not attempts. A dashboard user signing in correctly a
// hundred times a day never approaches a limit, while someone working through
// a password list reaches one quickly. Counting every attempt would throttle
// the legitimate traffic the server exists to serve.
//
// Every counter is a fixed window: it starts at the first failure and ends on
// schedule, and no later failure pushes its expiry further away. A sliding
// window would let an attacker who keeps hammering an account hold the block
// open indefinitely, which locks out the owner rather than the attacker and
// turns the protection into the denial of service. internal/cache's Incr
// applies the TTL on creation only for exactly this reason.
package ratelimit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"expo-open-ota/config"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/metrics"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// The scopes a counter can belong to. They are separate namespaces: a failure
// against one never advances another, so throttling an account cannot throttle
// an unrelated IP.
const (
	scopeLoginAccount   = "login_account"
	scopeLoginIP        = "login_ip"
	scopeRefreshIP      = "refresh_ip"
	scopePasswordChange = "password_change"
	scopeSSOCallbackIP  = "sso_callback_ip"
	scopeOAuthRegister  = "oauth_register_ip"
)

// The limits. They are constants and not configuration: an operator is not
// going to tune these, and every knob offered here is one more way to deploy
// the server with its authentication protection quietly switched off.
//
// Only rejected credentials are counted, so the ceilings are reached by
// guessing and not by use. Ten failures per account in fifteen minutes leaves
// room for someone who has forgotten which password they used, and fifty per
// address covers a whole office behind one NAT, since only their mistakes
// count against it.
const (
	defaultWindow     = 15 * time.Minute
	defaultPerAccount = 10
	defaultPerIP      = 50
)

// Decision is the answer to "may this credential be tried right now". When it
// is not allowed, RetryAfter is what the caller should put in the Retry-After
// header.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

// Limiter throttles the endpoints that accept a credential.
//
// Every method is safe on a nil receiver and allows the request in that case.
// The limiter is absent in stateless deployments and wherever the dashboard is
// switched off, and a nil check at each of a dozen call sites would be a dozen
// chances to forget one.
//
// Nil safety is why the limits are read through accountLimit and ipLimit rather
// than as fields. Calling a method on a nil pointer is fine in Go, but reading
// a field off one is not, so `l.check(scope, subject, l.perIP)` panics on the
// argument before the guard inside check is ever reached.
type Limiter struct {
	cache      cache.Cache
	secret     []byte
	window     time.Duration
	perAccount int
	perIP      int
}

// New builds the limiter with the constants above. The HMAC key is JWT_SECRET,
// which the server already refuses to boot without, so this introduces no new
// secret for an operator to manage.
func New(c cache.Cache) *Limiter {
	return &Limiter{
		cache:      c,
		secret:     []byte(config.GetEnv("JWT_SECRET")),
		window:     defaultWindow,
		perAccount: defaultPerAccount,
		perIP:      defaultPerIP,
	}
}

// CheckLogin reports whether this sign-in may be attempted at all, against both
// the account being named and the address doing the naming.
//
// It is deliberately called BEFORE the password is verified. Password hashing
// is expensive by design, so letting a throttled request reach it would leave
// the CPU cost of an attack in place after removing its point.
//
// The answer does not depend on whether the account exists. The counter is
// keyed on a hash of whatever email was submitted, present in the database or
// not, and both cases produce the same refusal. Throttling only real accounts
// would make the difference in behavior an oracle for enumerating them, which
// is the reason this is keyed the way it is.
func (l *Limiter) CheckLogin(email string, ip netip.Addr) Decision {
	if decision := l.check(scopeLoginAccount, normalizeEmail(email), l.accountLimit()); !decision.Allowed {
		return decision
	}
	return l.checkIP(scopeLoginIP, ip, l.ipLimit())
}

// RecordLoginFailure counts a rejected credential against both the account and
// the address.
//
// Call it for rejected credentials only. A database outage or a missing
// ADMIN_EMAIL is the operator's problem, not a sign-in attempt, and counting
// those would let an infrastructure blip lock every account in the deployment.
// This mirrors what the audit log already does in DashboardAuthService.
func (l *Limiter) RecordLoginFailure(email string, ip netip.Addr) {
	l.record(scopeLoginAccount, normalizeEmail(email), l.accountLimit())
	l.recordIP(scopeLoginIP, ip, l.ipLimit())
}

// RecordLoginSuccess clears the account's counter. Whoever just proved they
// hold the password should not inherit the failures that preceded them, which
// is usually their own typing.
//
// The address counter is deliberately left alone: one valid account is
// otherwise all an attacker needs to wipe the address counter between rounds.
func (l *Limiter) RecordLoginSuccess(email string) {
	l.reset(scopeLoginAccount, normalizeEmail(email))
}

// CheckRefresh and RecordRefreshFailure bound guessing at refresh tokens. There
// is no account to key on before the token is validated, so the address is the
// only subject available.
func (l *Limiter) CheckRefresh(ip netip.Addr) Decision {
	return l.checkIP(scopeRefreshIP, ip, l.ipLimit())
}

func (l *Limiter) RecordRefreshFailure(ip netip.Addr) {
	l.recordIP(scopeRefreshIP, ip, l.ipLimit())
}

// CheckPasswordChange and its recorders bound guessing at the CURRENT password
// on the change-password route. That route sits behind authentication, so the
// attacker here holds a stolen session and is trying to escalate it into the
// password itself. The subject is the user id, which is known and trustworthy
// by that point.
func (l *Limiter) CheckPasswordChange(userId string) Decision {
	return l.check(scopePasswordChange, userId, l.accountLimit())
}

func (l *Limiter) RecordPasswordChangeFailure(userId string) {
	l.record(scopePasswordChange, userId, l.accountLimit())
}

func (l *Limiter) RecordPasswordChangeSuccess(userId string) {
	l.reset(scopePasswordChange, userId)
}

// CheckSSOCallback and RecordSSOCallbackFailure bound hammering at the OIDC
// callback. The state parameter is already single-use, so this is about the
// volume of failed round-trips rather than about guessing a secret.
func (l *Limiter) CheckSSOCallback(ip netip.Addr) Decision {
	return l.checkIP(scopeSSOCallbackIP, ip, l.ipLimit())
}

func (l *Limiter) RecordSSOCallbackFailure(ip netip.Addr) {
	l.recordIP(scopeSSOCallbackIP, ip, l.ipLimit())
}

// CheckOAuthRegister and RecordOAuthRegister bound how many OAuth clients one
// address may create. Unlike every other scope this counts SUCCESSES: dynamic
// client registration is unauthenticated by design (RFC 7591), so the thing to
// bound is rows written, not credentials guessed.
func (l *Limiter) CheckOAuthRegister(ip netip.Addr) Decision {
	return l.checkIP(scopeOAuthRegister, ip, l.ipLimit())
}

func (l *Limiter) RecordOAuthRegister(ip netip.Addr) {
	l.recordIP(scopeOAuthRegister, ip, l.ipLimit())
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// accountLimit and ipLimit exist so a nil Limiter never has a field read off
// it. See the note on the type.
func (l *Limiter) accountLimit() int {
	if l == nil {
		return 0
	}
	return l.perAccount
}

func (l *Limiter) ipLimit() int {
	if l == nil {
		return 0
	}
	return l.perIP
}

// check reads a counter without advancing it.
//
// It fails OPEN. When the cache is unreachable Get returns an empty string,
// which parses to nothing and allows the request. The alternative locks every
// operator out of the dashboard for the duration of a Redis blip, which is a
// worse outcome than temporarily losing brute-force protection on a surface
// whose failures are still written to the audit log.
func (l *Limiter) check(scope, subject string, limit int) Decision {
	if l == nil || limit <= 0 || subject == "" {
		return Decision{Allowed: true}
	}
	count, err := strconv.Atoi(l.cache.Get(l.key(scope, subject)))
	if err != nil || count < limit {
		return Decision{Allowed: true}
	}
	metrics.TrackAuthThrottled(scope)
	return Decision{Allowed: false, RetryAfter: l.window}
}

func (l *Limiter) record(scope, subject string, limit int) {
	if l == nil || limit <= 0 || subject == "" {
		return
	}
	metrics.TrackAuthFailure(scope)
	_, _ = l.cache.Incr(l.key(scope, subject), int(l.window.Seconds()))
}

func (l *Limiter) reset(scope, subject string) {
	if l == nil || subject == "" {
		return
	}
	l.cache.Delete(l.key(scope, subject))
}

// checkIP and recordIP skip an address that did not parse. Folding every
// unparseable address onto one shared key would let a single client exhaust a
// counter that then blocks every other client whose address also failed to
// parse, so an unknown address is simply not counted.
func (l *Limiter) checkIP(scope string, ip netip.Addr, limit int) Decision {
	if !ip.IsValid() {
		return Decision{Allowed: true}
	}
	return l.check(scope, ip.String(), limit)
}

func (l *Limiter) recordIP(scope string, ip netip.Addr, limit int) {
	if !ip.IsValid() {
		return
	}
	l.record(scope, ip.String(), limit)
}

// key derives the cache key for a subject.
//
// The subject is hashed rather than stored. An email address is personal data
// and a cache is not where it should accumulate, least of all in a shared Redis
// an operator may not consider sensitive. The HMAC also stops anyone holding
// only cache access from confirming that a given address has an account here,
// since without the key they cannot compute the key to look for. The scope is
// mixed in so the same subject in two scopes cannot collide.
func (l *Limiter) key(scope, subject string) string {
	mac := hmac.New(sha256.New, l.secret)
	mac.Write([]byte(scope))
	mac.Write([]byte{0})
	mac.Write([]byte(subject))
	return "ratelimit:" + scope + ":" + hex.EncodeToString(mac.Sum(nil)[:16])
}
