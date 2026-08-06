package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"xprem/config"
	"xprem/internal/cache"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// A manifest request carries its runtime version and current update id in
// headers, so those two label values are whatever the client sent. Prometheus
// never drops a series once created, so an unbounded set of them is a remotely
// driven memory leak: maxSeriesPerMetric caps how many combinations a metric
// keeps, and maxLabelValueLen caps one value, since a header can be far larger
// than any real version or id.
const (
	maxSeriesPerMetric = 10000
	maxLabelValueLen   = 128
	overflowLabel      = "other"
)

// seriesLimiter remembers the label combinations a metric already carries, so a
// new one can be refused once that metric is at capacity.
type seriesLimiter struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (l *seriesLimiter) admit(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seen[key]; ok {
		return true
	}
	if len(l.seen) >= maxSeriesPerMetric {
		return false
	}
	if l.seen == nil {
		l.seen = make(map[string]struct{})
	}
	l.seen[key] = struct{}{}
	return true
}

func (l *seriesLimiter) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = nil
}

var (
	activeUsersLimiter      = &seriesLimiter{}
	updateErrorUsersLimiter = &seriesLimiter{}
)

// boundClientLabels folds the two client-supplied label values into
// overflowLabel when either is oversized or the metric is already at capacity.
// Callers must use the returned values for the cache key as well, so folded
// traffic shares one key instead of inventing one per request.
func boundClientLabels(limiter *seriesLimiter, appId, platform, runtime, branch, update string) (string, string) {
	if len(runtime) > maxLabelValueLen || len(update) > maxLabelValueLen {
		return overflowLabel, overflowLabel
	}
	if !limiter.admit(strings.Join([]string{appId, platform, runtime, branch, update}, "\x00")) {
		return overflowLabel, overflowLabel
	}
	return runtime, update
}

// All metrics are scoped by appId. In multi-app deployments (v2), two
// different apps can publish identically named branches / runtime versions,
// so we include appId in the label set AND in the Redis cache keys, if we
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
	if !cardinalityMetricsEnabled() {
		return
	}
	computedUpdate := update
	if computedUpdate == "" {
		computedUpdate = "unknown"
	}
	if appId == "" || clientId == "" || platform == "" || runtime == "" || branch == "" {
		return
	}
	// clientId is an unvalidated header, and it lands in a cache set, not a label.
	if len(clientId) > maxLabelValueLen {
		return
	}
	runtime, computedUpdate = boundClientLabels(updateErrorUsersLimiter, appId, platform, runtime, branch, computedUpdate)
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

// cardinalityMetricsEnabled reports whether anything can ever read the metrics
// that cost per-client state to maintain.
//
// TrackActiveUser and TrackUpdateErrorUsers are the only two Track* functions
// that remember individual clients: each Sadd's the client id into a cache set
// held for the metric's TTL (4h and 10m). On the local cache that set lives in
// the process, so its size tracks unique devices in the window — the "one entry
// per device on a fleet of a million" the LocalCache sweep comment warns about.
//
// /metrics is only registered when PROMETHEUS_ENABLED is true (see
// registerInfraRoutes), so with it unset that memory backs a gauge no one can
// scrape. This is the same reasoning as maxSeriesPerMetric above — bound what a
// remote caller can make the process remember — applied to the cache sets
// rather than to Prometheus series.
//
// Read live rather than cached at init: GetEnv reads the environment on each
// call, and the tests toggle PROMETHEUS_ENABLED between cases.
func cardinalityMetricsEnabled() bool {
	return config.GetEnv("PROMETHEUS_ENABLED") == "true"
}

func TrackActiveUser(appId, clientId, platform, runtime, branch, update string) {
	if !cardinalityMetricsEnabled() {
		return
	}
	if appId == "" || clientId == "" || platform == "" || branch == "" || update == "" || runtime == "" {
		return
	}
	if len(clientId) > maxLabelValueLen {
		return
	}
	runtime, update = boundClientLabels(activeUsersLimiter, appId, platform, runtime, branch, update)

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
	activeUsersLimiter.reset()
	updateErrorUsersLimiter.reset()
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
