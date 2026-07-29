package metrics_test

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"expo-open-ota/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func setupMetrics(t *testing.T) func() {
	os.Setenv("PROMETHEUS_ENABLED", "true")
	reg := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = reg
	prometheus.DefaultGatherer = reg
	metrics.ResetMetricsForTest()
	metrics.InitMetrics()
	// Note: the in-memory cache (Sadd/Scard state) is NOT reset between
	// tests, LocalCache.Clear() only resets the key/value map, not the
	// set map. Tests that assert exact unique-user counts must therefore
	// use identifier tuples (appId + branch + update + …) that are unique
	// to that single test. See the isolation tests below for the pattern.
	return func() {}
}

func getMetricValue(metricName string, labelFilter map[string]string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return 0
	}
	for _, mf := range mfs {
		if mf.GetName() == metricName {
			for _, m := range mf.Metric {
				match := true
				for key, filterValue := range labelFilter {
					found := false
					for _, label := range m.Label {
						if label.GetName() == key {
							matched, err := regexp.MatchString(filterValue, label.GetValue())
							if err == nil && matched {
								found = true
								break
							}
						}
					}
					if !found {
						match = false
						break
					}
				}
				if match {
					if m.Gauge != nil {
						return m.Gauge.GetValue()
					}
					if m.Counter != nil {
						return m.Counter.GetValue()
					}
				}
			}
		}
	}
	return 0
}

func getActiveUsers(appId, platform, runtime, branch, update string) float64 {
	return getMetricValue("active_users_total", map[string]string{
		"appId":    appId,
		"platform": platform,
		"runtime":  runtime,
		"branch":   branch,
		"update":   update,
	})
}

func getTotalUpdateDownloads(appId, platform, runtime, branch, update, updateType string) float64 {
	return getMetricValue("update_downloads_total", map[string]string{
		"appId":      appId,
		"platform":   platform,
		"runtime":    runtime,
		"branch":     branch,
		"update":     update,
		"updateType": updateType,
	})
}

func TestTrackUpdateDownload(t *testing.T) {
	teardown := setupMetrics(t)
	defer teardown()
	appId := "app-1"
	platform := "ios"
	runtime := "1.0.0"
	branch := "stable"
	update := "update42"
	updateType := "normal"
	metrics.TrackUpdateDownload(appId, platform, runtime, branch, update, updateType)
	val := getTotalUpdateDownloads(appId, platform, runtime, branch, update, updateType)
	if val != 1 {
		t.Errorf("Expected update_downloads_total to be 1, got %v", val)
	}
}

func TestTrackActiveUser(t *testing.T) {
	teardown := setupMetrics(t)
	defer teardown()
	appId := "app-1"
	clientId := "client1"
	platform := "ios"
	runtime := "1.0.0"
	branch := "stable"
	update := "update42"
	metrics.TrackActiveUser(appId, clientId, platform, runtime, branch, update)
	val := getActiveUsers(appId, platform, runtime, branch, update)
	if val != 1 {
		t.Errorf("Expected active_users_total to be 1, got %v", val)
	}
}

func TestGetActiveUsers(t *testing.T) {
	teardown := setupMetrics(t)
	defer teardown()
	appId := "app-1"
	clientId := "client1"
	platform := "ios"
	runtime := "1.0.0"
	branch := "stable"
	update := "update42"
	if got := getActiveUsers(appId, platform, runtime, branch, update); got != 0 {
		t.Errorf("Expected getActiveUsers to return 0, got %v", got)
	}
	metrics.TrackActiveUser(appId, clientId, platform, runtime, branch, update)
	if got := getActiveUsers(appId, platform, runtime, branch, update); got != 1 {
		t.Errorf("Expected getActiveUsers to return 1, got %v", got)
	}
	metrics.TrackActiveUser(appId, clientId, platform, runtime, branch, update)
	if got := getActiveUsers(appId, platform, runtime, branch, update); got != 1 {
		t.Errorf("Expected getActiveUsers to still be 1 (Gauge should not increment), got %v", got)
	}
	metrics.TrackActiveUser(appId, "client2", platform, runtime, branch, update)
	if got := getActiveUsers(appId, platform, runtime, branch, update); got != 2 {
		t.Errorf("Expected getActiveUsers to increment to 2, got %v", got)
	}
}

func TestGetTotalUpdateDownloadsByUpdate(t *testing.T) {
	teardown := setupMetrics(t)
	defer teardown()
	appId := "app-1"
	platform := "ios"
	runtime := "1.0.0"
	branch := "stable"
	update := "update42"
	updateType := "normal"
	if got := getTotalUpdateDownloads(appId, platform, runtime, branch, update, updateType); got != 0 {
		t.Errorf("Expected total update downloads to be 0, got %v", got)
	}
	metrics.TrackUpdateDownload(appId, platform, runtime, branch, update, updateType)
	if got := getTotalUpdateDownloads(appId, platform, runtime, branch, update, updateType); got != 1 {
		t.Errorf("Expected total update downloads to be 1, got %v", got)
	}
	metrics.TrackUpdateDownload(appId, platform, runtime, branch, update, updateType)
	if got := getTotalUpdateDownloads(appId, platform, runtime, branch, update, updateType); got != 2 {
		t.Errorf("Expected total update downloads to be 2, got %v", got)
	}
}

// Metrics for two different apps with identical branch/runtime/update must
// not merge, before v2 the cache keys were only scoped by branch so two apps
// with the same branch name would pollute each other's unique-user counts.
//
// The appIds here are intentionally unique to this test (not reused
// elsewhere) because the in-memory cache's set state persists across tests
// , using "app-1" would collide with whatever earlier tests left behind in
// the seen-users set for that appId.
func TestTrackActiveUser_IsolatedPerApp(t *testing.T) {
	teardown := setupMetrics(t)
	defer teardown()
	platform := "ios"
	runtime := "1.0.0"
	branch := "stable"
	update := "update42"

	metrics.TrackActiveUser("iso-active-a", "client1", platform, runtime, branch, update)
	metrics.TrackActiveUser("iso-active-b", "client1", platform, runtime, branch, update)

	if got := getActiveUsers("iso-active-a", platform, runtime, branch, update); got != 1 {
		t.Errorf("Expected iso-active-a active_users to be 1, got %v", got)
	}
	if got := getActiveUsers("iso-active-b", platform, runtime, branch, update); got != 1 {
		t.Errorf("Expected iso-active-b active_users to be 1, got %v", got)
	}
}

func TestTrackUpdateDownload_IsolatedPerApp(t *testing.T) {
	teardown := setupMetrics(t)
	defer teardown()
	platform := "ios"
	runtime := "1.0.0"
	branch := "stable"
	update := "update42"
	updateType := "normal"

	// Fresh-per-test appIds, same reason as TestTrackActiveUser_IsolatedPerApp.
	// The download metric is a Counter reset by ResetMetricsForTest, but using
	// unique ids keeps the two isolation tests symmetric and easy to read.
	metrics.TrackUpdateDownload("iso-dl-a", platform, runtime, branch, update, updateType)
	metrics.TrackUpdateDownload("iso-dl-a", platform, runtime, branch, update, updateType)
	metrics.TrackUpdateDownload("iso-dl-b", platform, runtime, branch, update, updateType)

	if got := getTotalUpdateDownloads("iso-dl-a", platform, runtime, branch, update, updateType); got != 2 {
		t.Errorf("Expected iso-dl-a downloads to be 2, got %v", got)
	}
	if got := getTotalUpdateDownloads("iso-dl-b", platform, runtime, branch, update, updateType); got != 1 {
		t.Errorf("Expected iso-dl-b downloads to be 1, got %v", got)
	}
}

func TestPrometheusHandler(t *testing.T) {
	teardown := setupMetrics(t)
	defer teardown()
	appId := "app-1"
	platform := "ios"
	runtime := "1.0.0"
	branch := "stable"
	update := "update42"
	updateType := "normal"
	metrics.TrackUpdateDownload(appId, platform, runtime, branch, update, updateType)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler := promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{})
	handler.ServeHTTP(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, "update_downloads_total") {
		t.Errorf("Expected update_downloads_total in metrics, got %s", body)
	}
	// Confirm the appId label is rendered in the exported format, protects
	// against a future refactor that drops the label without updating tests.
	if !strings.Contains(body, `appId="app-1"`) {
		t.Errorf("Expected appId=\"app-1\" label in metrics output, got %s", body)
	}
}
