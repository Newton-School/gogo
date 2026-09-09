package config

import (
	"errors"
	"net/http"
	"net/netip"

	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/security"
)

// Trust only explicitly configured proxy networks. Client-supplied forwarded
// headers outside these networks never become TLS or rate-limit authority.
func SecurityHeaders(settings conf.Values) (func(http.Handler) http.Handler, error) {
	var proxies []netip.Prefix
	for _, value := range settings.List("GOGO_TRUSTED_PROXIES") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, errors.New("GOGO_TRUSTED_PROXIES requires CIDR networks")
		}
		proxies = append(proxies, prefix.Masked())
	}
	return security.Headers(security.HeadersConfig{AllowedHosts: settings.List("GOGO_ALLOWED_HOSTS"), TrustedProxies: proxies})
}
