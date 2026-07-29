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

// The dashboard's own static build, called by a browser loading the single-page
// app. Anything that is not a known static extension falls back to index.html
// so client-side routing works on a hard refresh.
//
// AUTHENTICATION: none, and none is needed. What is served here is the
// compiled front-end, which is public by nature: it ships in the binary and
// contains no data. The application it boots holds no session either, it goes
// and asks /auth for one, then talks to /api. env.js only tells the page which
// API base URL to call.
//
// The traversal check is what matters instead: a request path is joined onto
// the dashboard directory, so it is verified to still be under it before the
// file is served.
func registerDashboardAssets(r *mux.Router) {
	// Resolved before the enable check, as it always has been: it is the call
	// that aborts the process when the executable path cannot be read, and
	// that failure is not something a disabled dashboard should hide.
	dashboardPath := getDashboardPath()

	if !dashutils.IsDashboardEnabled() {
		return
	}
	r.PathPrefix("/dashboard").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get env.js
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
