package security

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type proxyKey struct{}
type proxyInfo struct {
	secure bool
	ip     string
}

func IsSecure(r *http.Request) bool {
	p, ok := r.Context().Value(proxyKey{}).(proxyInfo)
	if ok {
		return p.secure
	}
	return r.TLS != nil
}
func ClientIP(r *http.Request) string {
	if p, ok := r.Context().Value(proxyKey{}).(proxyInfo); ok {
		return p.ip
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

type nonceKey struct{}

func CSPNonce(r *http.Request) string { v, _ := r.Context().Value(nonceKey{}).(string); return v }

type HeadersConfig struct {
	AllowedHosts                          []string
	TrustedProxies                        []netip.Prefix
	HTTPSOrigin                           string
	HSTS                                  time.Duration
	CSP                                   string
	CSPReportOnly                         bool
	FramePolicy, ReferrerPolicy           string
	CORSOrigins, CORSMethods, CORSHeaders []string
	CORSCredentials                       bool
}

func Headers(config HeadersConfig) (func(http.Handler) http.Handler, error) {
	// A constructed middleware owns its trust policy. Caller-side config reuse
	// must not mutate host/proxy/CORS allowlists or race with live requests.
	config.AllowedHosts = slices.Clone(config.AllowedHosts)
	config.TrustedProxies = slices.Clone(config.TrustedProxies)
	config.CORSOrigins = slices.Clone(config.CORSOrigins)
	config.CORSMethods = slices.Clone(config.CORSMethods)
	config.CORSHeaders = slices.Clone(config.CORSHeaders)
	if len(config.AllowedHosts) == 0 {
		return nil, errors.New("explicit allowed hosts required")
	}
	for _, host := range config.AllowedHosts {
		if host == "" || host == "*" || strings.ContainsAny(host, "/\\\r\n\t ") {
			return nil, errors.New("invalid allowed host")
		}
	}
	if config.HTTPSOrigin != "" {
		u, err := url.Parse(config.HTTPSOrigin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return nil, errors.New("invalid HTTPS redirect origin")
		}
	}
	for _, origin := range config.CORSOrigins {
		if !validOrigin(origin) {
			return nil, errors.New("invalid CORS origin")
		}
	}
	if config.CSP == "" {
		config.CSP = "default-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"
	}
	if config.FramePolicy == "" {
		config.FramePolicy = "DENY"
	}
	if config.ReferrerPolicy == "" {
		config.ReferrerPolicy = "same-origin"
	}
	if len(config.CORSMethods) == 0 {
		config.CORSMethods = []string{"GET", "HEAD", "OPTIONS"}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", config.FramePolicy)
			w.Header().Set("Referrer-Policy", config.ReferrerPolicy)
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			nonce, err := RandomToken(24)
			if err != nil {
				http.Error(w, "security service unavailable", 503)
				return
			}
			cspHeader := "Content-Security-Policy"
			if config.CSPReportOnly {
				cspHeader += "-Report-Only"
			}
			w.Header().Set(cspHeader, strings.ReplaceAll(config.CSP, "{nonce}", nonce))
			r = r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce))
			host := r.Host
			if h, _, e := net.SplitHostPort(host); e == nil {
				host = h
			}
			host = strings.TrimSuffix(strings.ToLower(host), ".")
			allowed := false
			for _, h := range config.AllowedHosts {
				h = strings.ToLower(h)
				if h == host || strings.HasPrefix(h, ".") && (host == h[1:] || strings.HasSuffix(host, h)) {
					allowed = true
					break
				}
			}
			if !allowed || strings.ContainsAny(r.Host, "/\\@\r\n\t ") {
				http.Error(w, "invalid host", 400)
				return
			}
			peer, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip, _ := netip.ParseAddr(peer)
			trusted := false
			for _, prefix := range config.TrustedProxies {
				trusted = trusted || prefix.Contains(ip)
			}
			info := proxyInfo{secure: r.TLS != nil, ip: peer}
			if trusted {
				proto := r.Header.Get("X-Forwarded-Proto")
				if proto == "https" || proto == "http" {
					info.secure = proto == "https"
				}
				chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
				for i := len(chain) - 1; i >= 0; i-- {
					candidate, e := netip.ParseAddr(strings.TrimSpace(chain[i]))
					if e != nil {
						break
					}
					info.ip = candidate.String()
					isProxy := false
					for _, p := range config.TrustedProxies {
						isProxy = isProxy || p.Contains(candidate)
					}
					if !isProxy {
						break
					}
				}
			}
			r = r.WithContext(context.WithValue(r.Context(), proxyKey{}, info))
			if info.secure && config.HSTS > 0 {
				w.Header().Set("Strict-Transport-Security", "max-age="+strconv.FormatInt(int64(config.HSTS/time.Second), 10))
			}
			if !info.secure && config.HTTPSOrigin != "" {
				http.Redirect(w, r, config.HTTPSOrigin+r.URL.RequestURI(), http.StatusPermanentRedirect)
				return
			}
			origin := r.Header.Get("Origin")
			if origin != "" && !sameOrigin(r, origin) {
				w.Header().Add("Vary", "Origin")
				if !slices.Contains(config.CORSOrigins, origin) {
					if r.Method == "OPTIONS" {
						http.Error(w, "origin not allowed", 403)
						return
					}
				} else {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					if config.CORSCredentials {
						w.Header().Set("Access-Control-Allow-Credentials", "true")
					}
					if r.Method == "OPTIONS" && r.Header.Get("Access-Control-Request-Method") != "" {
						w.Header().Add("Vary", "Access-Control-Request-Method")
						w.Header().Add("Vary", "Access-Control-Request-Headers")
						if !slices.Contains(config.CORSMethods, r.Header.Get("Access-Control-Request-Method")) {
							http.Error(w, "method not allowed", 403)
							return
						}
						for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
							header = strings.TrimSpace(header)
							if header != "" && !slices.ContainsFunc(config.CORSHeaders, func(h string) bool { return strings.EqualFold(h, header) }) {
								http.Error(w, "header not allowed", 403)
								return
							}
						}
						w.Header().Set("Access-Control-Allow-Methods", strings.Join(config.CORSMethods, ", "))
						w.Header().Set("Access-Control-Allow-Headers", strings.Join(config.CORSHeaders, ", "))
						w.WriteHeader(204)
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// SafeNext permits only same-origin relative redirects. It rejects browser
// backslash normalization and encoded control characters before redirecting.
func SafeNext(value, fallback string) string {
	u, err := url.Parse(value)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") || strings.ContainsAny(u.Path, "\\\r\n") || strings.HasPrefix(u.Path, "//") {
		return fallback
	}
	return value
}
