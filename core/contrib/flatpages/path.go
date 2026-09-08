package flatpages

import (
	"io/fs"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// normalizePath is shared by storage and request lookup. It canonicalizes
// spelling, never path structure: case and a trailing slash remain significant.
// HTTP callers discard the query separately; stored paths cannot contain one.
func normalizePath(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > MaxURLBytes || !utf8.ValidString(raw) || raw[0] != '/' || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "?#\\") {
		return "", ErrInvalid
	}
	for _, r := range raw {
		if r <= ' ' || unicode.IsControl(r) {
			return "", ErrInvalid
		}
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || !utf8.ValidString(decoded) {
		return "", ErrInvalid
	}
	for _, r := range decoded {
		if r == '\\' || unicode.IsControl(r) {
			return "", ErrInvalid
		}
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return "", ErrInvalid
		}
	}
	for i := 0; i+2 < len(decoded); i++ {
		if decoded[i] == '%' && pathHex(decoded[i+1]) && pathHex(decoded[i+2]) {
			b := pathHexByte(decoded[i+1], decoded[i+2])
			// A nested percent rejects arbitrarily deeper chains in this one
			// bounded pass. Encoded non-ASCII bytes could form new controls.
			if b <= ' ' || b >= 127 || strings.ContainsRune("/\\.%?#", rune(b)) {
				return "", ErrInvalid
			}
		}
	}
	var out strings.Builder
	out.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b == '%' {
			b = pathHexByte(raw[i+1], raw[i+2]) // PathUnescape checked every escape.
			if b == '/' || b == '\\' {
				return "", ErrInvalid
			}
			if pathUnreserved(b) {
				out.WriteByte(b)
			} else {
				writePathEscape(&out, b)
			}
			i += 2
		} else if b >= 128 {
			writePathEscape(&out, b)
		} else if pathUnreserved(b) || strings.ContainsRune("/:@!$&'()*+,;=", rune(b)) {
			out.WriteByte(b)
		} else {
			return "", ErrInvalid
		}
		if out.Len() > MaxURLBytes {
			return "", ErrInvalid
		}
	}
	return out.String(), nil
}

// validateDraft is pure. The domain owner separately validates or allocates ID.
func validateDraft(value Draft) (Draft, error) {
	if value.Title == "" || len(value.Title) > 4*MaxTitleRunes || !utf8.ValidString(value.Title) || utf8.RuneCountInString(value.Title) > MaxTitleRunes || strings.ContainsRune(value.Title, 0) || len(value.Content) > MaxContentBytes || !utf8.ValidString(value.Content) || strings.ContainsRune(value.Content, 0) {
		return Draft{}, ErrInvalid
	}
	if value.TemplateName != "" && !validTemplateName(value.TemplateName) {
		return Draft{}, ErrInvalid
	}
	path, err := normalizePath(value.URL)
	if err != nil {
		return Draft{}, err
	}
	value.URL = path
	return value, nil
}

func validTemplateName(name string) bool {
	if len(name) == 0 || len(name) > MaxTemplateBytes || !utf8.ValidString(name) || !fs.ValidPath(name) || strings.ContainsAny(name, "\\#") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func writePathEscape(out *strings.Builder, b byte) {
	out.WriteByte('%')
	out.WriteByte("0123456789ABCDEF"[b>>4])
	out.WriteByte("0123456789ABCDEF"[b&15])
}
func pathUnreserved(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~", rune(b))
}
func pathHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}
func pathHexByte(a, b byte) byte {
	digit := func(b byte) byte {
		if b >= '0' && b <= '9' {
			return b - '0'
		}
		return (b | 32) - 'a' + 10
	}
	return digit(a)<<4 | digit(b)
}
