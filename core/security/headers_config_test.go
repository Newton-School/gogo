package security

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
)

func TestHeadersOwnEveryConfigurationAllowlist(t *testing.T) {
	config := HeadersConfig{
		AllowedHosts:    []string{"allowed.test"},
		TrustedProxies:  []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		CORSOrigins:     []string{"https://reader.test"},
		CORSMethods:     []string{"GET"},
		CORSHeaders:     []string{"X-Original"},
		CORSCredentials: true,
	}
	middleware, err := Headers(config)
	if err != nil {
		t.Fatal(err)
	}
	// Constructing middleware snapshots configuration before a handler is
	// wrapped. Reusing the caller's slices must not rewrite a live trust policy.
	config.AllowedHosts[0] = "injected.test"
	config.TrustedProxies[0] = netip.MustParsePrefix("198.51.100.0/24")
	config.CORSOrigins[0] = "https://injected.test"
	config.CORSMethods[0] = "DELETE"
	config.CORSHeaders[0] = "X-Injected"
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsSecure(r) {
			w.Header().Set("X-Secure", "yes")
		}
		w.Header().Set("X-Client-IP", ClientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name, host, peer, origin, method, header string
		want                                     int
		secure, grant                            bool
	}{
		{name: "original-host", host: "allowed.test", peer: "198.51.100.7:1234", want: 204},
		{name: "injected-host", host: "injected.test", peer: "198.51.100.7:1234", want: 400},
		{name: "original-proxy", host: "allowed.test", peer: "192.0.2.7:1234", want: 204, secure: true},
		{name: "original-cors", host: "allowed.test", peer: "198.51.100.7:1234", origin: "https://reader.test", method: "GET", header: "X-Original", want: 204, grant: true},
		{name: "injected-origin", host: "allowed.test", peer: "198.51.100.7:1234", origin: "https://injected.test", method: "GET", header: "X-Original", want: 403},
		{name: "injected-method", host: "allowed.test", peer: "198.51.100.7:1234", origin: "https://reader.test", method: "DELETE", header: "X-Original", want: 403, grant: true},
		{name: "injected-header", host: "allowed.test", peer: "198.51.100.7:1234", origin: "https://reader.test", method: "GET", header: "X-Injected", want: 403, grant: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := http.MethodGet
			if tc.origin != "" {
				method = http.MethodOptions
			}
			r := httptest.NewRequest(method, "http://"+tc.host+"/", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-For", "203.0.113.9")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
				r.Header.Set("Access-Control-Request-Method", tc.method)
				r.Header.Set("Access-Control-Request-Headers", tc.header)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || (w.Header().Get("X-Secure") == "yes") != tc.secure || (w.Header().Get("Access-Control-Allow-Origin") != "") != tc.grant {
				t.Fatal("configuration mutation rewrote trusted policy", w.Code, w.Header())
			}
			if tc.secure && w.Header().Get("X-Client-IP") != "203.0.113.9" {
				t.Fatal(w.Header())
			}
			if tc.name == "original-host" && w.Header().Get("X-Client-IP") != "198.51.100.7" {
				t.Fatal("untrusted proxy changed client IP", w.Header())
			}
		})
	}
}

func TestHeadersSnapshotIsolatedDuringConcurrentRequests(t *testing.T) {
	config := HeadersConfig{AllowedHosts: []string{"allowed.test"}, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, CORSOrigins: []string{"https://reader.test"}, CORSMethods: []string{"GET"}, CORSHeaders: []string{"X-Original"}}
	middleware, err := Headers(config)
	if err != nil {
		t.Fatal(err)
	}
	h := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			config.AllowedHosts[0] = "injected.test"
			config.TrustedProxies[0] = netip.MustParsePrefix("198.51.100.0/24")
			config.CORSOrigins[0] = "https://injected.test"
			config.CORSMethods[0] = "DELETE"
			config.CORSHeaders[0] = "X-Injected"
		}
	}()
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("OPTIONS", "http://allowed.test/", nil)
			r.RemoteAddr = "192.0.2.4:1000"
			r.Header.Set("Origin", "https://reader.test")
			r.Header.Set("Access-Control-Request-Method", "GET")
			r.Header.Set("Access-Control-Request-Headers", "X-Original")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://reader.test" {
				t.Error(w.Code, w.Header())
			}
		}()
	}
	wg.Wait()
}
