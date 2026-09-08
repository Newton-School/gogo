package sitemaps

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxLocationBytes = 2047

// urlPolicy contains constructor-owned configuration only. Publication authority
// is deliberately absent: even a same-origin URL needs the application's policy.
type urlPolicy struct {
	origin    *url.URL
	directory string
	allowed   map[string]bool
}

func newURLPolicy(config Config) (*urlPolicy, error) {
	if len(config.AdditionalOrigins) > 64 {
		return nil, ErrLimit
	}
	origin, err := sitemapOrigin(config.Origin)
	if err != nil {
		return nil, err
	}
	directory, err := safeDirectory(config.Directory)
	if err != nil {
		return nil, err
	}
	if len(origin.String())+len(directory) > maxLocationBytes {
		return nil, ErrLimit
	}
	p := &urlPolicy{origin: origin, directory: directory, allowed: map[string]bool{origin.String(): true}}
	for _, raw := range config.AdditionalOrigins {
		other, err := sitemapOrigin(raw)
		if err != nil {
			return nil, err
		}
		if p.allowed[other.String()] {
			return nil, ErrInvalid
		}
		p.allowed[other.String()] = true
	}
	return p, nil
}

// Directory is also inserted into a route pattern, so permit static ASCII
// segments only; never interpret escaping, dot segments or route converters.
func safeDirectory(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	if len(raw) > maxLocationBytes {
		return "", ErrLimit
	}
	if raw[0] != '/' || strings.Contains(raw, "//") {
		return "", ErrInvalid
	}
	for _, segment := range strings.Split(strings.Trim(raw, "/"), "/") {
		if segment == "." || segment == ".." {
			return "", ErrInvalid
		}
		for _, b := range []byte(segment) {
			if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.') {
				return "", ErrInvalid
			}
		}
	}
	if !strings.HasSuffix(raw, "/") {
		raw += "/"
	}
	if len(raw) > maxLocationBytes {
		return "", ErrLimit
	}
	return raw, nil
}

func boundedURI(raw string) error {
	if len(raw) > maxLocationBytes {
		return ErrLimit
	}
	if raw == "" || !utf8.ValidString(raw) {
		return ErrInvalid
	}
	for _, c := range raw {
		if c <= 32 || c == 127 || c == '\\' {
			return ErrInvalid
		}
	}
	return nil
}

func sitemapOrigin(raw string) (*url.URL, error) {
	if err := boundedURI(raw); err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return nil, ErrInvalid
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, ErrInvalid
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if strings.HasPrefix(u.Host, "[") {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return nil, ErrInvalid
		}
		host = ip.String()
	} else if strings.Contains(host, ":") {
		return nil, ErrInvalid
	} else if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	} else {
		if len(host) == 0 || len(host) > 253 {
			return nil, ErrInvalid
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, ErrInvalid
			}
			for _, b := range []byte(label) {
				if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-') {
					return nil, ErrInvalid
				}
			}
		}
	}
	port := u.Port()
	if port != "" {
		if len(port) > 5 {
			return nil, ErrInvalid
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return nil, ErrInvalid
		}
		if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
			port = ""
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, ErrInvalid
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return &url.URL{Scheme: scheme, Host: host}, nil
}

func (p *urlPolicy) location(raw string, alternate bool) (string, error) {
	if p == nil || p.origin == nil {
		return "", ErrInvalid
	}
	if err := boundedURI(raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || strings.Contains(raw, "#") {
		return "", ErrInvalid
	}
	if !u.IsAbs() {
		if u.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
			return "", ErrInvalid
		}
		u.Scheme, u.Host = p.origin.Scheme, p.origin.Host
	}
	origin, err := sitemapOrigin(u.Scheme + "://" + u.Host)
	if err != nil {
		return "", err
	}
	if !p.allowed[origin.String()] || !alternate && origin.String() != p.origin.String() {
		return "", ErrInvalid
	}
	u.Scheme, u.Host = origin.Scheme, origin.Host
	if u.Path == "" {
		u.Path = "/"
	}
	if !validSitemapPath(u.Path, u.EscapedPath()) || !alternate && !strings.HasPrefix(u.Path, p.directory) {
		return "", ErrInvalid
	}
	u.RawQuery, err = sitemapQuery(u.RawQuery)
	if err != nil {
		return "", err
	}
	value := u.String()
	if len(value) > maxLocationBytes {
		return "", ErrLimit
	}
	for _, b := range []byte(value) {
		if b >= 128 {
			return "", ErrInvalid
		}
	}
	return value, nil
}

func validSitemapPath(decoded, encoded string) bool {
	if !utf8.ValidString(decoded) || !strings.HasPrefix(decoded, "/") || strings.Contains(decoded, "//") {
		return false
	}
	for _, c := range decoded {
		if c < 32 || c == 127 || c == '\\' {
			return false
		}
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	// Encoded separators and recursively encoded structural escapes produce
	// different directory scopes under different server/proxy decoding orders.
	encoded = strings.ToLower(encoded)
	decoded = strings.ToLower(decoded)
	return !strings.Contains(encoded, "%2f") && !strings.Contains(encoded, "%5c") &&
		!strings.Contains(decoded, "%2e") && !strings.Contains(decoded, "%2f") &&
		!strings.Contains(decoded, "%5c") && !strings.Contains(decoded, "%25")
}

func sitemapQuery(raw string) (string, error) {
	decoded, err := url.QueryUnescape(raw)
	if err != nil || !utf8.ValidString(decoded) {
		return "", ErrInvalid
	}
	for _, c := range decoded {
		if c < 32 || c == 127 || c == '\\' {
			return "", ErrInvalid
		}
	}
	var out strings.Builder
	const hex = "0123456789ABCDEF"
	for _, b := range []byte(raw) {
		if b < 128 && (b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~!$&'()*+,;=:@/?%", rune(b))) {
			out.WriteByte(b)
		} else {
			out.WriteByte('%')
			out.WriteByte(hex[b>>4])
			out.WriteByte(hex[b&15])
		}
		if out.Len() > maxLocationBytes {
			return "", ErrLimit
		}
	}
	return out.String(), nil
}
