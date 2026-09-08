package static

import (
	"io/fs"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxPathBytes = 2048

func validOwner(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if c > 127 || !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}
func validPath(name string) bool {
	if len(name) == 0 || len(name) > maxPathBytes || !utf8.ValidString(name) || name == "." || !fs.ValidPath(name) || path.Clean(name) != name || strings.HasSuffix(name, "/") {
		return false
	}
	for _, c := range name {
		if c < 32 || c == 127 || strings.ContainsRune("\\?#:*\"<>|", c) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
	}
	return true
}
func validDirectory(name string) bool {
	if name == "" || len(name) > 4096 || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\\") || filepath.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
func ignoredPath(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

type urlPrefix struct{ base, path, scheme, host string }

func parsePrefix(raw string) (urlPrefix, error) {
	if raw == "" || len(raw) > maxPathBytes || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\x00\\\r\n\t") || !strings.HasSuffix(raw, "/") {
		return urlPrefix{}, ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return urlPrefix{}, ErrInvalid
	}
	if u.Scheme == "" {
		if u.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
			return urlPrefix{}, ErrInvalid
		}
	} else if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Hostname() == "" {
		return urlPrefix{}, ErrInvalid
	}
	if u.Path == "" || !strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "//") || path.Clean(u.Path)+"/" != u.Path && u.Path != "/" {
		return urlPrefix{}, ErrInvalid
	}
	for _, c := range u.Path {
		if c < 33 || c > 126 || strings.ContainsRune("?#%", c) {
			return urlPrefix{}, ErrInvalid
		}
	}
	return urlPrefix{base: u.String(), path: u.Path, scheme: u.Scheme, host: u.Host}, nil
}
func assetURL(prefix urlPrefix, name string) string {
	parts := strings.Split(name, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return prefix.base + strings.Join(parts, "/")
}
