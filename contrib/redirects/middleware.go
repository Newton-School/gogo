package redirects

import (
	"bytes"
	"net/http"
	"net/url"

	"github.com/Newton-School/gogo/contrib/sites"
)

type Options struct {
	AllowUnsafeTargets bool
}

func Middleware(store Store, siteStore sites.Store, options Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			if recorder.status != http.StatusNotFound {
				return
			}
			redirect, ok := CurrentRedirect(r.Context(), r, store, siteStore)
			if !ok {
				recorder.replay()
				return
			}
			if redirect.NewPath == "" {
				http.Error(w, "gone", http.StatusGone)
				return
			}
			if !options.AllowUnsafeTargets && unsafeTarget(redirect.NewPath) {
				recorder.replay()
				return
			}
			status := http.StatusFound
			if redirect.Permanent {
				status = http.StatusMovedPermanently
			}
			http.Redirect(w, r, redirect.NewPath, status)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	return r.body.Write(body)
}

func (r *statusRecorder) replay() {
	r.ResponseWriter.WriteHeader(r.status)
	_, _ = r.ResponseWriter.Write(r.body.Bytes())
}

func unsafeTarget(target string) bool {
	parsed, err := url.Parse(target)
	return err != nil || parsed.IsAbs()
}
