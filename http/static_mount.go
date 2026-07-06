package http

import (
	"fmt"
	nethttp "net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Newton-School/gogo/conf"
)

// StaticMountConfig configures development static/media serving.
type StaticMountConfig struct {
	Env               string
	URLPrefix         string
	Root              string
	AllowInProduction bool
}

// NewStaticMount creates a static/media handler.
func NewStaticMount(config StaticMountConfig) (Handler, error) {
	if config.Env == "production" && !config.AllowInProduction {
		return nil, fmt.Errorf("%w: refusing to serve %s in production", ErrStaticMount, config.URLPrefix)
	}
	if strings.TrimSpace(config.URLPrefix) == "" || !strings.HasPrefix(config.URLPrefix, "/") {
		return nil, fmt.Errorf("%w: URLPrefix must start with /", ErrStaticMount)
	}
	if !strings.HasSuffix(config.URLPrefix, "/") {
		config.URLPrefix += "/"
	}
	if strings.TrimSpace(config.Root) == "" {
		return nil, fmt.Errorf("%w: Root is required", ErrStaticMount)
	}

	root, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStaticMount, err)
	}

	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.Method != nethttp.MethodGet && r.Method != nethttp.MethodHead {
			response := MethodNotAllowed([]string{nethttp.MethodGet, nethttp.MethodHead}, nil)
			_ = response.Write(w)
			return
		}
		requestPath := r.URL.EscapedPath()
		if !strings.HasPrefix(requestPath, config.URLPrefix) {
			nethttp.NotFound(w, r)
			return
		}

		target, ok := staticTarget(root, config.URLPrefix, requestPath)
		if !ok {
			nethttp.Error(w, "Bad Request", nethttp.StatusBadRequest)
			return
		}

		info, err := os.Stat(target)
		if err != nil || info.IsDir() {
			nethttp.NotFound(w, r)
			return
		}
		nethttp.ServeFile(w, r, target)
	}), nil
}

// StaticFilesMount creates a handler from static settings.
func StaticFilesMount(settings conf.Settings) (Handler, error) {
	return NewStaticMount(StaticMountConfig{
		Env:       settings.Env,
		URLPrefix: settings.StaticURL,
		Root:      settings.StaticRoot,
	})
}

// MediaFilesMount creates a handler from media settings.
func MediaFilesMount(settings conf.Settings) (Handler, error) {
	return NewStaticMount(StaticMountConfig{
		Env:       settings.Env,
		URLPrefix: settings.MediaURL,
		Root:      settings.MediaRoot,
	})
}

// MountStatic registers a static/media handler on a standard ServeMux.
func MountStatic(mux *nethttp.ServeMux, config StaticMountConfig) error {
	handler, err := NewStaticMount(config)
	if err != nil {
		return err
	}
	prefix := config.URLPrefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	mux.Handle(prefix, handler)
	return nil
}

func staticTarget(root, prefix, requestPath string) (string, bool) {
	raw := strings.TrimPrefix(requestPath, prefix)
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", false
	}
	if hasParentTraversal(decoded) {
		return "", false
	}
	clean := filepath.Clean(string(filepath.Separator) + decoded)
	target := filepath.Join(root, clean)

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", false
	}
	if absTarget != root && !strings.HasPrefix(absTarget, root+string(filepath.Separator)) {
		return "", false
	}
	return absTarget, true
}

func hasParentTraversal(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}
