package files_test

import (
	"net/http"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/files"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/urls"
)

func ExampleNewDownloadHandler() {
	// Compile-only composition. Inject a configured Service with explicit owner
	// scope/current read policy, and real read-only request authentication/grants.
	configure := func(service *files.Service, authorize func(*http.Request) error) (*ghttp.Server, error) {
		download, err := files.NewDownloadHandler(service, files.DownloadOptions{
			ID: func(r *http.Request) (string, error) {
				// Gogo's built-in UUID converter is a scalar trusted context value.
				// The download callback intentionally has no stdlib PathValue state.
				id, ok := urls.Param(r, "id").(string)
				if !ok {
					return "", files.ErrInvalidOwner
				}
				return id, nil
			},
			Authorize: authorize,
			Filename:  "attachment.bin",
			Timeout:   30 * time.Second,
		})
		if err != nil {
			return nil, err
		}
		router, err := urls.New(urls.Path("/private/files/<uuid:id>/", download, "private-file", "GET", "HEAD"))
		if err != nil {
			return nil, err
		}
		return ghttp.NewServer(ghttp.ServerConfig{
			Handler: router,
			// This bypasses the server's buffering TimeoutHandler, not authority.
			// Do not wrap this path with compression or a buffering middleware.
			Streaming: func(r *http.Request) bool {
				return r.URL != nil && strings.HasPrefix(r.URL.Path, "/private/files/")
			},
		})
	}
	_ = configure
}
