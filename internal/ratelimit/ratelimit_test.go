package ratelimit

import (
	"expo-open-ota/internal/cache"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestLimiter(perAccount, perIP int) *Limiter {
	return &Limiter{
		cache:      cache.NewLocalCache(),
		secret:     []byte("test-secret"),
		window:     15 * time.Minute,
		perAccount: perAccount,
		perIP:      perIP,
	}
}

func addr(t *testing.T, raw string) netip.Addr {
	t.Helper()
	parsed, err := netip.ParseAddr(raw)
	require.NoError(t, err)
	return parsed
}

func TestLoginIsRefusedOnceTheAccountLimitIsReached(t *testing.T) {
	limiter := newTestLimiter(3, 100)
	email, ip := "axel@example.com", addr(t, "198.51.100.1")

	for attempt := 1; attempt <= 3; attempt++ {
		require.True(t, limiter.CheckLogin(email, ip).Allowed, "attempt %d should still be allowed", attempt)
		limiter.RecordLoginFailure(email, ip)
	}

	decision := limiter.CheckLogin(email, ip)
	require.False(t, decision.Allowed)
	require.Equal(t, 15*time.Minute, decision.RetryAfter)
}

// The limiter is never told whether an account exists, and this is the test
// that pins that down: it holds no repository and takes no such argument, so an
// unknown address is counted and refused exactly like a known one. A response
// that differed between the two would let anyone enumerate the accounts on a
// deployment by watching which emails start being throttled.
func TestRefusalIsIdenticalForAnUnknownAccount(t *testing.T) {
	limiter := newTestLimiter(2, 100)
	ip := addr(t, "198.51.100.1")

	known, unknown := "axel@example.com", "nobody-here@example.com"
	for range 2 {
		limiter.RecordLoginFailure(known, ip)
		limiter.RecordLoginFailure(unknown, ip)
	}

	require.Equal(t, limiter.CheckLogin(known, ip), limiter.CheckLogin(unknown, ip))
	require.False(t, limiter.CheckLogin(unknown, ip).Allowed)
}

func TestCountersAreSeparatePerAccount(t *testing.T) {
	limiter := newTestLimiter(2, 100)
	ip := addr(t, "198.51.100.1")

	limiter.RecordLoginFailure("axel@example.com", ip)
	limiter.RecordLoginFailure("axel@example.com", ip)

	require.False(t, limiter.CheckLogin("axel@example.com", ip).Allowed)
	require.True(t, limiter.CheckLogin("someone-else@example.com", ip).Allowed)
}

// Signing in successfully clears the account's own counter, so a person who
// mistyped their password four times is not carrying those four failures for
// the rest of the window.
func TestASuccessfulLoginClearsTheAccountCounter(t *testing.T) {
	limiter := newTestLimiter(3, 100)
	email, ip := "axel@example.com", addr(t, "198.51.100.1")

	limiter.RecordLoginFailure(email, ip)
	limiter.RecordLoginFailure(email, ip)
	limiter.RecordLoginSuccess(email)

	for attempt := 1; attempt <= 3; attempt++ {
		require.True(t, limiter.CheckLogin(email, ip).Allowed, "the counter should have restarted, attempt %d", attempt)
		limiter.RecordLoginFailure(email, ip)
	}
	require.False(t, limiter.CheckLogin(email, ip).Allowed)
}

// The address counter is deliberately NOT cleared by a success. An attacker who
// holds one valid account would otherwise sign into it between rounds and wipe
// the address counter every time, which makes the per-address limit worthless
// against exactly the person it is there to stop.
func TestASuccessfulLoginDoesNotClearTheAddressCounter(t *testing.T) {
	limiter := newTestLimiter(100, 3)
	ip := addr(t, "198.51.100.1")

	for range 3 {
		limiter.RecordLoginFailure("victim@example.com", ip)
	}
	limiter.RecordLoginSuccess("attacker@example.com")

	require.False(t, limiter.CheckLogin("another-victim@example.com", ip).Allowed,
		"the address counter must survive a successful sign-in on another account")
}

func TestTheAddressLimitTripsIndependentlyOfTheAccount(t *testing.T) {
	limiter := newTestLimiter(100, 3)
	ip := addr(t, "198.51.100.1")

	// Each attempt names a different account, so no account counter ever
	// climbs. Only the address ties them together.
	limiter.RecordLoginFailure("a@example.com", ip)
	limiter.RecordLoginFailure("b@example.com", ip)
	limiter.RecordLoginFailure("c@example.com", ip)

	require.False(t, limiter.CheckLogin("d@example.com", ip).Allowed)
	require.True(t, limiter.CheckLogin("d@example.com", addr(t, "203.0.113.9")).Allowed,
		"a different address must not inherit the block")
}

// The distributed case, and the reason the per-account counter exists at all.
// An attacker with a botnet presents a fresh address for every guess, so no
// address counter ever climbs and a per-address limit alone would never fire.
// What ties the attempts together is the account being guessed.
func TestADistributedAttackOnOneAccountIsStoppedByTheAccountLimit(t *testing.T) {
	limiter := newTestLimiter(3, 100)
	email := "axel@example.com"

	for i := range 3 {
		from := addr(t, netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)}).String())
		require.True(t, limiter.CheckLogin(email, from).Allowed)
		limiter.RecordLoginFailure(email, from)
	}

	// A fourth address, never seen before, inherits the account's block.
	require.False(t, limiter.CheckLogin(email, addr(t, "203.0.113.200")).Allowed)
}

// Failures arriving at the same instant must all be counted. A counter built on
// read-then-write loses most of them under this load, which would let a
// parallel attack run far past its ceiling: internal/cache's Incr is atomic for
// exactly this reason, and this is the limiter-level guard on that property.
func TestConcurrentFailuresDoNotOvershootTheLimit(t *testing.T) {
	const attempts = 50
	limiter := newTestLimiter(attempts, 1000)
	email, ip := "axel@example.com", addr(t, "198.51.100.1")

	var ready, done sync.WaitGroup
	start := make(chan struct{})
	for range attempts {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			limiter.RecordLoginFailure(email, ip)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	require.False(t, limiter.CheckLogin(email, ip).Allowed,
		"%d concurrent failures should have reached the limit of %d; increments were lost", attempts, attempts)
}

// Scopes are separate namespaces. Without the scope mixed into the key, a user
// id that happened to equal an email would share a counter across two unrelated
// endpoints, and failures on one would throttle the other.
func TestScopesDoNotShareCounters(t *testing.T) {
	limiter := newTestLimiter(2, 100)
	subject := "axel@example.com"

	limiter.RecordPasswordChangeFailure(subject)
	limiter.RecordPasswordChangeFailure(subject)

	require.False(t, limiter.CheckPasswordChange(subject).Allowed)
	require.True(t, limiter.CheckLogin(subject, addr(t, "198.51.100.1")).Allowed)
}

// An address that did not parse is not counted at all. Folding every
// unparseable address onto one shared key would let a single client exhaust it
// and take every other unknown-address caller down with them.
func TestAnInvalidAddressIsNotCounted(t *testing.T) {
	limiter := newTestLimiter(100, 2)
	var invalid netip.Addr

	for range 5 {
		limiter.RecordLoginFailure("a@example.com", invalid)
	}

	require.True(t, limiter.CheckLogin("b@example.com", invalid).Allowed)
}

// A nil limiter allows everything and must never panic. This is not
// hypothetical: the SSO handler is constructed with a nil limiter in its own
// tests, and an earlier version of this package read the limit off the receiver
// as a call argument, which panicked before reaching any nil guard.
func TestANilLimiterAllowsEverythingWithoutPanicking(t *testing.T) {
	var limiter *Limiter
	ip := addr(t, "198.51.100.1")

	require.True(t, limiter.CheckLogin("axel@example.com", ip).Allowed)
	require.True(t, limiter.CheckRefresh(ip).Allowed)
	require.True(t, limiter.CheckPasswordChange("u-1").Allowed)
	require.True(t, limiter.CheckSSOCallback(ip).Allowed)

	require.NotPanics(t, func() {
		limiter.RecordLoginFailure("axel@example.com", ip)
		limiter.RecordLoginSuccess("axel@example.com")
		limiter.RecordRefreshFailure(ip)
		limiter.RecordPasswordChangeFailure("u-1")
		limiter.RecordPasswordChangeSuccess("u-1")
		limiter.RecordSSOCallbackFailure(ip)
	})
}

// The subject is hashed, never stored. A shared Redis is not somewhere an
// operator expects to find their users' email addresses, and anyone holding
// only cache access should not be able to confirm that a given address has an
// account here.
func TestTheSubjectNeverAppearsInTheKey(t *testing.T) {
	limiter := newTestLimiter(10, 50)
	email := "axel@example.com"

	key := limiter.key(scopeLoginAccount, email)

	require.NotContains(t, key, email)
	require.NotContains(t, key, "axel")
	require.NotContains(t, key, "example.com")
	require.Contains(t, key, scopeLoginAccount, "the scope stays readable so the keyspace can be inspected")
}

// Two deployments with different JWT_SECRETs derive different keys for the same
// person, so a shared cache cannot be used to correlate them.
func TestTheKeyDependsOnTheSecret(t *testing.T) {
	first := newTestLimiter(10, 50)
	second := newTestLimiter(10, 50)
	second.secret = []byte("a-different-secret")

	require.NotEqual(t,
		first.key(scopeLoginAccount, "axel@example.com"),
		second.key(scopeLoginAccount, "axel@example.com"))
}

// An empty subject is not a subject. Counting one would put every request that
// failed to identify itself on a single shared counter.
func TestAnEmptySubjectIsNotCounted(t *testing.T) {
	limiter := newTestLimiter(2, 100)

	for range 5 {
		limiter.record(scopeLoginAccount, "", limiter.accountLimit())
	}

	require.True(t, limiter.check(scopeLoginAccount, "", limiter.accountLimit()).Allowed)
}

// The limiter fails OPEN. Locking every operator out of the dashboard for the
// duration of a cache outage is worse than briefly losing brute-force
// protection on a surface whose failures the audit log still records.
func TestAnUnreadableCounterAllowsTheRequest(t *testing.T) {
	limiter := newTestLimiter(1, 1)
	ttl := 60
	// What a cache miss and what a foreign value both look like: nothing that
	// parses as a count.
	require.NoError(t, limiter.cache.Set(limiter.key(scopeLoginAccount, "axel@example.com"), "not-a-number", &ttl))

	require.True(t, limiter.CheckLogin("axel@example.com", addr(t, "198.51.100.1")).Allowed)
}

func TestNewReadsTheDefaults(t *testing.T) {
	t.Setenv("JWT_SECRET", "secret")
	limiter := New(cache.NewLocalCache())

	require.Equal(t, defaultWindow, limiter.window)
	require.Equal(t, defaultPerAccount, limiter.perAccount)
	require.Equal(t, defaultPerIP, limiter.perIP)
	require.False(t, strings.Contains(limiter.key(scopeLoginAccount, "axel@example.com"), "axel"))
}
