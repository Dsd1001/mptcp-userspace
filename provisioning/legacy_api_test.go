// Historical pre-1.2 API regressions. This handler is test-only and is NOT
// compiled into the shipped controller. Tunnel API acceptance is separate.
package main

import (
	"io"
	"net/http"
)

func (a *app) legacyHandlerForTest() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/config/", a.publicConfig)
	mux.HandleFunc("/v1/bundle/", a.publicBundle)
	mux.HandleFunc("/v1/device/pair", a.devicePair)
	mux.HandleFunc("/v1/device/poll", a.devicePoll)
	mux.HandleFunc("/v1/device/report", a.deviceReport)
	mux.HandleFunc("/v1/device/unpair", a.deviceUnpair)
	mux.HandleFunc("/admin", a.requireAdmin(a.adminPage))
	mux.HandleFunc("/admin/", a.requireAdmin(a.adminPage))
	mux.HandleFunc("/admin/api/system", a.requireAdmin(a.adminSystem))
	mux.HandleFunc("/admin/api/password", a.requireAdmin(a.adminPassword))
	mux.HandleFunc("/admin/api/profiles", a.requireAdmin(a.adminProfiles))
	mux.HandleFunc("/admin/api/profiles/", a.requireAdmin(a.adminProfileByID))
	mux.HandleFunc("/admin/api/bundles", a.requireAdmin(a.adminBundles))
	mux.HandleFunc("/admin/api/bundles/", a.requireAdmin(a.adminBundleByID))
	mux.HandleFunc("/admin/api/devices", a.requireAdmin(a.adminDevices))
	mux.HandleFunc("/admin/api/devices/", a.requireAdmin(a.adminDeviceByID))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		mux.ServeHTTP(w, r)
	})
}
