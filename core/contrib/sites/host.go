package sites

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// NormalizeDomain accepts ASCII DNS/A-labels or unbracketed IP literals, never
// a URL or an authority with a port. Unicode IDNA conversion is not performed.
// DNS case and one trailing dot normalize; IPv6 uses its canonical IP spelling.
func NormalizeDomain(value string) (string, error) {
	if len(value) == 0 || len(value) > 254 {
		return "", ErrInvalidHost
	}
	for _, b := range []byte(value) {
		if b <= 32 || b >= 127 {
			return "", ErrInvalidHost
		}
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		if addr.Zone() != "" {
			return "", ErrInvalidHost
		}
		return addr.String(), nil
	}
	value = strings.TrimSuffix(strings.ToLower(value), ".")
	if value == "" || len(value) > 253 {
		return "", ErrInvalidHost
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrInvalidHost
		}
		for _, b := range []byte(label) {
			if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-') {
				return "", ErrInvalidHost
			}
		}
	}
	return value, nil
}

func requestDomain(authority string) (string, error) {
	if len(authority) < 1 || len(authority) > 262 {
		return "", ErrInvalidHost
	}
	host := authority
	if strings.HasPrefix(authority, "[") {
		if strings.HasSuffix(authority, "]") {
			host = authority[1 : len(authority)-1]
		} else {
			var port string
			var err error
			host, port, err = net.SplitHostPort(authority)
			if err != nil || !validPort(port) {
				return "", ErrInvalidHost
			}
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", ErrInvalidHost
		}
	} else if strings.Contains(authority, ":") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil || !validPort(port) {
			return "", ErrInvalidHost
		}
	}
	return NormalizeDomain(host)
}

func validPort(port string) bool {
	if len(port) < 1 || len(port) > 5 {
		return false
	}
	for _, c := range []byte(port) {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

type hostRule struct {
	name   string
	suffix bool
}

func allowedRules(values []string) ([]hostRule, error) {
	if len(values) > 256 {
		return nil, ErrConfiguration
	}
	seen := map[hostRule]bool{}
	rules := make([]hostRule, 0, len(values))
	for _, value := range values {
		rule := hostRule{suffix: strings.HasPrefix(value, ".")}
		if rule.suffix {
			value = value[1:]
		}
		var err error
		rule.name, err = NormalizeDomain(value)
		if err != nil {
			return nil, ErrConfiguration
		}
		if _, err := netip.ParseAddr(rule.name); err == nil && rule.suffix {
			return nil, ErrConfiguration
		}
		if seen[rule] {
			return nil, ErrConfiguration
		}
		seen[rule] = true
		rules = append(rules, rule)
	}
	return rules, nil
}

func (s *resolverState) host(authority string) (string, error) {
	domain, err := requestDomain(authority)
	if err != nil {
		return "", err
	}
	for _, rule := range s.hosts {
		if domain == rule.name || rule.suffix && strings.HasSuffix(domain, "."+rule.name) {
			return domain, nil
		}
	}
	return "", ErrInvalidHost
}
