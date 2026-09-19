package admin

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Pinned Django 5.2.17 static tree; see THIRD_PARTY_NOTICES.md.
const djangoAssetVersion = "5.2.17-e802ada38b3e"

func (s *Site) djangoAssetURL() string {
	return s.config.Prefix + "assets/django/" + djangoAssetVersion + "/"
}

func (s *Site) djangoAsset(requestPath string) ([]byte, string, string, bool, bool) {
	prefix := s.djangoAssetURL()
	if !strings.HasPrefix(requestPath, prefix) {
		return nil, "", "", false, false
	}
	name := strings.TrimPrefix(requestPath, prefix)
	if !fs.ValidPath(name) || strings.ContainsAny(name, "\\\x00") {
		return nil, "", "", false, false
	}
	contentType := map[string]string{
		".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
		".svg": "image/svg+xml", ".txt": "text/plain; charset=utf-8",
	}[path.Ext(name)]
	if path.Base(name) == "LICENSE" || strings.HasPrefix(path.Base(name), "LICENSE-") {
		contentType = "text/plain; charset=utf-8"
	}
	if contentType == "" {
		return nil, "", "", false, false
	}
	body, err := embedded.ReadFile("internal/assets/django/" + name)
	if err != nil {
		return nil, "", "", false, false
	}
	return body, fmt.Sprintf("%x", sha256.Sum256(body)), contentType, false, true
}
