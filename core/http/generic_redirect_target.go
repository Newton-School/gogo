package http

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxGenericRedirectURI = 2048

// genericRedirectTarget is syntax validation, never an authority grant. Every
// absolute destination requires its own application policy, even on the same
// host. Query values remain data: encoded URLs are not recursively parsed.
func genericRedirectTarget(raw, forwardedQuery string) (string, bool, error) {
	if raw == "" || !genericRedirectRaw(raw) || !genericRedirectRaw(forwardedQuery) {
		return "", false, ErrUnavailable
	}
	beforeFragment, fragment, hasFragment := strings.Cut(raw, "#")
	beforeQuery, query, hasQuery := strings.Cut(beforeFragment, "?")
	path, origin := beforeQuery, ""
	if !strings.HasPrefix(path, "/") {
		_, authority, found := strings.Cut(path, "://")
		if !found {
			return "", false, ErrUnavailable
		}
		originText := path
		path = "/"
		if index := strings.IndexByte(authority, '/'); index >= 0 {
			path = authority[index:]
			originText = beforeQuery[:len(beforeQuery)-len(path)]
		}
		var err error
		origin, err = genericRedirectOrigin(originText)
		if err != nil {
			return "", false, ErrUnavailable
		}
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "", false, ErrUnavailable
	}
	path, err := genericRedirectComponent(path, true)
	if err != nil {
		return "", false, ErrUnavailable
	}
	query, err = genericRedirectComponent(query, false)
	if err != nil {
		return "", false, ErrUnavailable
	}
	fragment, err = genericRedirectComponent(fragment, false)
	if err != nil {
		return "", false, ErrUnavailable
	}
	forwardedQuery, err = genericRedirectComponent(forwardedQuery, false)
	if err != nil {
		return "", false, ErrUnavailable
	}
	if forwardedQuery != "" {
		if query != "" {
			query += "&"
		}
		query += forwardedQuery
		hasQuery = true
	}
	// Every component is bounded before assembling the final header value.
	size := len(origin) + len(path) + len(query) + len(fragment)
	if hasQuery {
		size++
	}
	if hasFragment {
		size++
	}
	if size > maxGenericRedirectURI {
		return "", false, ErrUnavailable
	}
	var output strings.Builder
	output.Grow(size)
	output.WriteString(origin)
	output.WriteString(path)
	if hasQuery {
		output.WriteByte('?')
		output.WriteString(query)
	}
	if hasFragment {
		output.WriteByte('#')
		output.WriteString(fragment)
	}
	return output.String(), origin != "", nil
}

func genericRedirectRaw(raw string) bool {
	if len(raw) > maxGenericRedirectURI || !utf8.ValidString(raw) {
		return false
	}
	for _, c := range raw {
		if c <= ' ' || c == '\\' || unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// genericRedirectOrigin accepts ASCII DNS/A-labels and canonical IP addresses.
// It normalizes case, one DNS trailing dot and default ports, but refuses
// browser-specific integer, short, octal and hexadecimal IPv4 interpretations.
func genericRedirectOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", ErrUnavailable
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrUnavailable
	}
	host := u.Hostname()
	if strings.HasPrefix(u.Host, "[") {
		addr, err := netip.ParseAddr(host)
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", ErrUnavailable
		}
		host = addr.String()
	} else {
		if len(host) == 0 || len(host) > 254 || strings.Contains(host, ":") {
			return "", ErrUnavailable
		}
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		if host == "" || len(host) > 253 {
			return "", ErrUnavailable
		}
		if addr, err := netip.ParseAddr(host); err == nil {
			if !addr.Is4() || addr.Zone() != "" {
				return "", ErrUnavailable
			}
			host = addr.String()
		} else {
			for _, label := range strings.Split(host, ".") {
				if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
					return "", ErrUnavailable
				}
				for _, c := range label {
					if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
						return "", ErrUnavailable
					}
				}
			}
			if genericRedirectNumericHost(host) {
				return "", ErrUnavailable
			}
		}
	}
	port := u.Port()
	if port != "" {
		if len(port) > 5 {
			return "", ErrUnavailable
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
			return "", ErrUnavailable
		}
		if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
			port = ""
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", ErrUnavailable
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

func genericRedirectNumericHost(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	decimal := true
	for _, c := range last {
		decimal = decimal && c >= '0' && c <= '9'
	}
	if decimal {
		return true
	}
	if strings.HasPrefix(last, "0x") {
		for _, c := range last[2:] {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return false
			}
		}
		return true
	}
	return false
}

func genericRedirectComponent(raw string, path bool) (string, error) {
	decoded, err := url.PathUnescape(raw)
	if err != nil || !utf8.ValidString(decoded) {
		return "", ErrUnavailable
	}
	for _, c := range decoded {
		if c == '\\' || unicode.IsControl(c) {
			return "", ErrUnavailable
		}
	}
	if path {
		for _, segment := range strings.Split(decoded, "/") {
			if segment == "." || segment == ".." {
				return "", ErrUnavailable
			}
		}
		for i := 0; i+2 < len(raw); i++ {
			if raw[i] == '%' && (genericRedirectHexByte(raw[i+1], raw[i+2]) == '/' || genericRedirectHexByte(raw[i+1], raw[i+2]) == '\\') {
				return "", ErrUnavailable
			}
		}
		// One extra decoded percent catches deeper chains without recursive
		// unbounded decoding. Query and fragment values are not path structure.
		for i := 0; i+2 < len(decoded); i++ {
			if decoded[i] == '%' && genericRedirectHex(decoded[i+1]) && genericRedirectHex(decoded[i+2]) {
				b := genericRedirectHexByte(decoded[i+1], decoded[i+2])
				if b <= ' ' || b == 127 || strings.ContainsRune("/\\.%?#", rune(b)) {
					return "", ErrUnavailable
				}
			}
		}
	}
	var output strings.Builder
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b >= 128 {
			output.WriteByte('%')
			output.WriteByte("0123456789ABCDEF"[b>>4])
			output.WriteByte("0123456789ABCDEF"[b&15])
		} else if genericRedirectUnreserved(b) || strings.ContainsRune(":@!$&'()*+,;=/%", rune(b)) || !path && b == '?' {
			output.WriteByte(b)
		} else {
			return "", ErrUnavailable
		}
		if output.Len() > maxGenericRedirectURI {
			return "", ErrUnavailable
		}
	}
	return output.String(), nil
}

func genericRedirectUnreserved(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~", rune(b))
}

func genericRedirectHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func genericRedirectHexByte(a, b byte) byte {
	digit := func(b byte) byte {
		if b >= '0' && b <= '9' {
			return b - '0'
		}
		return (b | 32) - 'a' + 10
	}
	return digit(a)<<4 | digit(b)
}
