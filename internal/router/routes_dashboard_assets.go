package infrastructure

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"xprem/config"
	dashutils "xprem/internal/dashboard"

	"github.com/gorilla/mux"
)

func getDashboardPath() string {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Error getting executable path: %v", err)
	}
	exeDir := filepath.Dir(exePath)

	if strings.Contains(exePath, "/var/folders/") || strings.Contains(exePath, "Temp") {
		workingDir, _ := os.Getwd()
		return filepath.Join(workingDir, "apps", "dashboard", "dist")
	}
	return filepath.Join(exeDir, "apps", "dashboard", "dist")
}

// registerDashboardAssets serves the dashboard's static build. Anything that
// is not a known static extension falls back to index.html so client-side
// routing works on a hard refresh. No authentication: the compiled front-end
// is public by nature and holds no session of its own.
func registerDashboardAssets(r *mux.Router) {
	dashboardPath := getDashboardPath()

	if !dashutils.IsDashboardEnabled() {
		return
	}
	if config.GetEnv("DASHBOARD_ROOT_REDIRECT") == "true" {
		// 302 and not 301: a permanent redirect on "/" would be cached by browsers
		// even after the dashboard is disabled or the root gains a real purpose.
		r.Path("/").Methods(http.MethodGet).HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dashboard/", http.StatusFound)
		})
	}
	r.PathPrefix("/dashboard").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dashboard/env.js" {
			w.Header().Set("Content-Type", "application/javascript")
			baseURL := config.GetEnv("BASE_URL")
			if baseURL == "" {
				baseURL = "http://localhost:3000"
			}
			w.Write([]byte(fmt.Sprintf("window.env = { VITE_OTA_API_URL: '%s' };", baseURL)))
			return
		}
		if r.URL.Path == "/dashboard" {
			target := "/dashboard/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		staticExtensions := []string{".css", ".js", ".svg", ".png", ".json", ".ico"}
		for _, ext := range staticExtensions {
			if len(r.URL.Path) > len(ext) && r.URL.Path[len(r.URL.Path)-len(ext):] == ext {
				filePath := filepath.Join(dashboardPath, r.URL.Path[len("/dashboard/"):])
				if !strings.HasPrefix(filePath, dashboardPath) {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
				http.ServeFile(w, r, filePath)
				return
			}
		}
		filePath := filepath.Join(dashboardPath, "index.html")
		fmt.Println("Serving file", filePath)
		http.ServeFile(w, r, filePath)
	}))
}
