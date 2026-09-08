package redirects

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/contrib/sites"
)

// targetURI separates the emitted URI from the next local request identity.
// External targets deliberately have no Key: this package never follows them.
type targetURI struct {
	Location, Key, Origin string
	External              bool
}

type uriParts struct {
	origin, path, query, fragment string
	hasQuery, hasFragment         bool
}

func boundedURI(raw string) error {
	if len(raw) > MaxURIBytes || !utf8.ValidString(raw) {
		return ErrInvalid
	}
	for _, c := range raw {
		if c <= ' ' || c == '\\' || unicode.IsControl(c) {
			return ErrInvalid
		}
	}
	return nil
}

// normalizeOrigin accepts HTTP(S) authorities only. Host normalization shares
// Sites' A-label/IP policy, but preserves nondefault ports for origin identity.
// Noncanonical browser IPv4 number syntax is refused rather than reinterpreted.
func normalizeOrigin(raw string) (string, error) {
	if err := boundedURI(raw); err != nil || raw == "" {
		return "", ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Host == "" || u.RawPath != "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return "", ErrInvalid
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrInvalid
	}
	host, err := sites.NormalizeDomain(u.Hostname())
	if err != nil {
		return "", ErrInvalid
	}
	if strings.HasPrefix(u.Host, "[") {
		addr, err := netip.ParseAddr(host)
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", ErrInvalid
		}
		host = addr.String()
	} else {
		if strings.Contains(host, ":") {
			return "", ErrInvalid
		}
		if addr, err := netip.ParseAddr(host); err == nil {
			if !addr.Is4() || addr.Zone() != "" {
				return "", ErrInvalid
			}
			host = addr.String()
		} else if numericFinalLabel(host) {
			// Browsers interpret integer, shortened, octal and hexadecimal IPv4
			// spellings differently from ordinary DNS names. Even a mixed DNS
			// name ending in a numeric label is not a portable HTTP authority.
			return "", ErrInvalid
		}
	}
	port := u.Port()
	if port != "" {
		if len(port) > 5 {
			return "", ErrInvalid
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", ErrInvalid
		}
		if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
			port = ""
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", ErrInvalid
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

func numericFinalLabel(host string) bool {
	_, last, found := strings.Cut(host, ".")
	if !found {
		last = host
	} else {
		last = host[strings.LastIndexByte(host, '.')+1:]
	}
	if last == "" {
		return false
	}
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

// parseOldPath preserves exact escaped path/query spelling. Bare '?' is not an
// old-path distinction, matching Django's nonempty QUERY_STRING full-path rule.
func parseOldPath(raw string) (string, error) {
	parts, err := parseURI(raw, false)
	if err != nil {
		return "", err
	}
	return parts.key(), nil
}

func appendSlashKey(raw string) (string, bool, error) {
	parts, err := parseURI(raw, false)
	if err != nil {
		return "", false, err
	}
	if strings.HasSuffix(parts.path, "/") {
		return parts.key(), false, nil
	}
	parts.path += "/"
	key := parts.key()
	if len(key) > MaxURIBytes {
		return "", false, ErrInvalid
	}
	return key, true, nil
}

// validateStoredTarget validates without querying or granting an external
// destination. Empty is the only Gone representation; whitespace is not empty.
func validateStoredTarget(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	parts, err := parseURI(raw, true)
	if err != nil {
		return "", err
	}
	return parts.location(), nil
}

func parseTarget(raw, sourceKey, origin string, preserve bool) (targetURI, error) {
	base, err := normalizeOrigin(origin)
	if err != nil {
		return targetURI{}, err
	}
	source, err := parseURI(sourceKey, false)
	if err != nil {
		return targetURI{}, err
	}
	parts, err := parseURI(raw, true)
	if err != nil {
		return targetURI{}, err
	}
	if preserve && !parts.hasQuery && source.query != "" {
		parts.query, parts.hasQuery = source.query, true
	}
	result := targetURI{Location: parts.location(), Origin: parts.origin}
	if len(result.Location) > MaxURIBytes {
		return targetURI{}, ErrInvalid
	}
	if result.Origin == "" {
		result.Origin = base
	}
	result.External = result.Origin != base
	if !result.External {
		result.Key = parts.key()
	}
	return result, nil
}

func (p uriParts) key() string {
	key := p.path
	if p.query != "" {
		key += "?" + p.query
	}
	return key
}

func (p uriParts) location() string {
	location := p.origin + p.path
	if p.hasQuery {
		location += "?" + p.query
	}
	if p.hasFragment {
		location += "#" + p.fragment
	}
	return location
}

func parseURI(raw string, target bool) (uriParts, error) {
	if err := boundedURI(raw); err != nil || raw == "" {
		return uriParts{}, ErrInvalid
	}
	beforeFragment, fragment, hasFragment := strings.Cut(raw, "#")
	beforeQuery, query, hasQuery := strings.Cut(beforeFragment, "?")
	if hasFragment && !target {
		return uriParts{}, ErrInvalid
	}
	parts := uriParts{path: beforeQuery, query: query, fragment: fragment, hasQuery: hasQuery, hasFragment: hasFragment}
	if !strings.HasPrefix(beforeQuery, "/") {
		if !target {
			return uriParts{}, ErrInvalid
		}
		_, authority, found := strings.Cut(beforeQuery, "://")
		if !found {
			return uriParts{}, ErrInvalid
		}
		originText := beforeQuery
		parts.path = "/"
		if index := strings.IndexByte(authority, '/'); index >= 0 {
			parts.path = authority[index:]
			originText = beforeQuery[:len(beforeQuery)-len(parts.path)]
		}
		var err error
		parts.origin, err = normalizeOrigin(originText)
		if err != nil {
			return uriParts{}, err
		}
	}
	if !strings.HasPrefix(parts.path, "/") || strings.HasPrefix(parts.path, "//") {
		return uriParts{}, ErrInvalid
	}
	var err error
	parts.path, err = uriComponent(parts.path, true)
	if err != nil {
		return uriParts{}, err
	}
	parts.query, err = uriComponent(parts.query, false)
	if err != nil {
		return uriParts{}, err
	}
	parts.fragment, err = uriComponent(parts.fragment, false)
	if err != nil {
		return uriParts{}, err
	}
	if len(parts.location()) > MaxURIBytes {
		return uriParts{}, ErrInvalid
	}
	return parts, nil
}

func uriComponent(raw string, path bool) (string, error) {
	decoded, err := url.PathUnescape(raw)
	if err != nil || !utf8.ValidString(decoded) {
		return "", ErrInvalid
	}
	for _, c := range decoded {
		if c == '\\' || unicode.IsControl(c) {
			return "", ErrInvalid
		}
	}
	if path {
		for _, segment := range strings.Split(decoded, "/") {
			if segment == "." || segment == ".." {
				return "", ErrInvalid
			}
		}
		for i := 0; i+2 < len(raw); i++ {
			if raw[i] == '%' && (hexByte(raw[i+1], raw[i+2]) == '/' || hexByte(raw[i+1], raw[i+2]) == '\\') {
				return "", ErrInvalid
			}
		}
		// Refuse nested escapes that could become structure or controls under
		// another decoder. A nested percent catches arbitrarily deeper chains
		// in this single bounded pass. Query values are deliberately not paths.
		for i := 0; i+2 < len(decoded); i++ {
			if decoded[i] == '%' && isHex(decoded[i+1]) && isHex(decoded[i+2]) {
				b := hexByte(decoded[i+1], decoded[i+2])
				if b <= ' ' || b == 127 || strings.ContainsRune("/\\.%?#", rune(b)) {
					return "", ErrInvalid
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
		} else if unreserved(b) || strings.ContainsRune(":@!$&'()*+,;=/%", rune(b)) || !path && b == '?' {
			output.WriteByte(b)
		} else {
			return "", ErrInvalid
		}
		if output.Len() > MaxURIBytes {
			return "", ErrInvalid
		}
	}
	return output.String(), nil
}

// cycleKey normalizes only comparison identity, never the SQL lookup key.
// Query order, duplicate members, '+' and reserved escapes remain significant.
func cycleKey(key, origin string) (string, error) {
	base, err := normalizeOrigin(origin)
	if err != nil {
		return "", err
	}
	key, err = parseOldPath(key)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	output.WriteString(base)
	for i := 0; i < len(key); i++ {
		if key[i] == '%' {
			b := hexByte(key[i+1], key[i+2])
			if unreserved(b) {
				output.WriteByte(b)
			} else {
				output.WriteByte('%')
				output.WriteByte("0123456789ABCDEF"[b>>4])
				output.WriteByte("0123456789ABCDEF"[b&15])
			}
			i += 2
		} else {
			output.WriteByte(key[i])
		}
	}
	return output.String(), nil
}

func unreserved(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~", rune(b))
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func hexByte(a, b byte) byte {
	digit := func(b byte) byte {
		if b >= '0' && b <= '9' {
			return b - '0'
		}
		return (b | 32) - 'a' + 10
	}
	return digit(a)<<4 | digit(b)
}
