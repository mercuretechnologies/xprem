package metrics

import (
	"fmt"
	"net/http"
	"xprem/internal/cache"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// All metrics are scoped by appId. In multi-app deployments (v2), two
// different apps can publish identically named branches / runtime versions,
// so we include appId in the label set AND in the Redis cache keys — if we
// didn't, the seen-users sets would merge across apps and skew the unique
// counts.
var (
	activeUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "active_users_total",
			Help: "Total number of unique active users per appId, platform, runtime version, branch and update",
		},
		[]string{"appId", "platform", "runtime", "branch", "update"},
	)

	globalActiveUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "global_active_users_total",
			Help: "Total number of unique active users per appId and platform across all runtime versions, branches and updates",
		},
		[]string{"appId", "platform"},
	)

	updateDownloadsVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "update_downloads_total",
			Help: "Total number of update downloads per appId, platform, runtime version, branch and update",
		},
		[]string{"appId", "platform", "runtime", "branch", "update", "updateType"},
	)

	updateErrorUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "update_error_users_total",
			Help: "Total number of users who encountered an error for a given appId, platform, runtime version, branch and update",
		},
		[]string{"appId", "platform", "runtime", "branch", "update"},
	)

	// Authentication throttling (internal/ratelimit). The only label is the
	// scope, a fixed set of five constants. Labelling by email or by client
	// address is the first thing an operator will ask for and is exactly what
	// must not happen: the cardinality is unbounded, so one credential-stuffing
	// run invents a time series per address and takes the scrape down with it,
	// and it writes personal data into a store rarely treated as holding any.
	// Who was throttled is a question for the audit log, which records it.
	authThrottledVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "auth_throttled_attempts_total",
			Help: "Total number of authentication attempts refused because the subject was over its rate limit, per scope",
		},
		[]string{"scope"},
	)

	// The denominator for the counter above. Without it a throttle count has no
	// scale, and there is no telling a limit that is working from one set so
	// low it is throttling ordinary mistyped passwords.
	authFailedVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "auth_failed_attempts_total",
			Help: "Total number of rejected authentication credentials counted against a rate limit, per scope",
		},
		[]string{"scope"},
	)
)

// TrackAuthThrottled counts one request refused by the rate limiter.
func TrackAuthThrottled(scope string) {
	authThrottledVec.WithLabelValues(scope).Inc()
}

// TrackAuthFailure counts one rejected credential.
func TrackAuthFailure(scope string) {
	authFailedVec.WithLabelValues(scope).Inc()
}

func InitMetrics() {
	prometheus.MustRegister(activeUsersVec)
	prometheus.MustRegister(updateDownloadsVec)
	prometheus.MustRegister(updateErrorUsersVec)
	prometheus.MustRegister(globalActiveUsersVec)
	prometheus.MustRegister(authThrottledVec)
	prometheus.MustRegister(authFailedVec)
}

func CleanupMetrics() {
	prometheus.Unregister(activeUsersVec)
	prometheus.Unregister(updateDownloadsVec)
	prometheus.Unregister(updateErrorUsersVec)
	prometheus.Unregister(globalActiveUsersVec)
	prometheus.Unregister(authThrottledVec)
	prometheus.Unregister(authFailedVec)
}

func TrackUpdateErrorUsers(appId, clientId, platform, runtime, branch, update string) {
	computedUpdate := update
	if computedUpdate == "" {
		computedUpdate = "unknown"
	}
	if appId == "" || clientId == "" || platform == "" || runtime == "" || branch == "" {
		return
	}
	resolvedCache := cache.GetCache()
	key := fmt.Sprintf("update_error_users:%s:%s:%s:%s:%s", appId, branch, platform, runtime, computedUpdate)
	ttl := 600

	_ = resolvedCache.Sadd(key, []string{clientId}, &ttl)

	count, err := resolvedCache.Scard(key)
	if err != nil {
		return
	}
	updateErrorUsersVec.WithLabelValues(appId, platform, runtime, branch, computedUpdate).Set(float64(count))
}

func TrackActiveUser(appId, clientId, platform, runtime, branch, update string) {
	if appId == "" || clientId == "" || platform == "" || branch == "" || update == "" || runtime == "" {
		return
	}

	resolvedCache := cache.GetCache()
	activeUserKey := fmt.Sprintf("seen_users:%s:%s:%s:%s:%s", appId, branch, platform, runtime, update)
	ttl := 14400

	_ = resolvedCache.Sadd(activeUserKey, []string{clientId}, &ttl)

	count, err := resolvedCache.Scard(activeUserKey)
	if err != nil {
		return
	}
	activeUsersVec.WithLabelValues(appId, platform, runtime, branch, update).Set(float64(count))

	globalActiveUserKey := fmt.Sprintf("global_active_users:%s:%s", appId, platform)
	_ = resolvedCache.Sadd(globalActiveUserKey, []string{clientId}, &ttl)
	count, err = resolvedCache.Scard(globalActiveUserKey)
	if err != nil {
		return
	}
	globalActiveUsersVec.WithLabelValues(appId, platform).Set(float64(count))
}

func TrackUpdateDownload(appId, platform, runtime, branch, update, updateType string) {
	if appId == "" || update == "" || platform == "" || branch == "" {
		return
	}
	updateDownloadsVec.WithLabelValues(appId, platform, runtime, branch, update, updateType).Inc()
}

func PrometheusHandler() http.Handler {
	return promhttp.Handler()
}

func ResetMetricsForTest() {
	activeUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "active_users_total",
			Help: "Total number of unique active users per appId, platform, runtime version, branch and update",
		},
		[]string{"appId", "platform", "runtime", "branch", "update"},
	)
	updateDownloadsVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "update_downloads_total",
			Help: "Total number of update downloads per appId, platform, runtime version, branch and update",
		},
		[]string{"appId", "platform", "runtime", "branch", "update", "updateType"},
	)
	updateErrorUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "update_error_users_total",
			Help: "Total number of users who encountered an error for a given appId, platform, runtime version, branch and update",
		},
		[]string{"appId", "platform", "runtime", "branch", "update"},
	)
	globalActiveUsersVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "global_active_users_total",
			Help: "Total number of unique active users per appId and platform across all runtime versions, branches and updates",
		},
		[]string{"appId", "platform"},
	)
}
