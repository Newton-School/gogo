package integration_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/files"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/urls"
)

func nativeDownloadRouter(t *testing.T, service *files.Service, authorize func(*http.Request) error) *urls.Router {
	t.Helper()
	h, err := files.NewDownloadHandler(service, files.DownloadOptions{ID: func(r *http.Request) (string, error) {
		id, ok := urls.Param(r, "id").(string)
		if !ok {
			return "", files.ErrInvalidOwner
		}
		return id, nil
	}, Authorize: authorize, Filename: "private.txt"})
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("/private/files/<uuid:id>/", h, "private-file", "GET", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func TestFilesDownloadNativeAuthorityAndHTTP(t *testing.T) {
	f := newNativeFileFixture(t)
	s := f.service(t)
	input := nativeFileInput(t)
	info, err := s.StoreValidated(context.Background(), input, strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	router := nativeDownloadRouter(t, s, func(r *http.Request) error {
		requests++
		if r.Header.Get("Authorization") != "Bearer fixture" {
			return auth.ErrUnauthenticated
		}
		return nil
	})
	perform := func(method string, header http.Header, id string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/private/files/"+id+"/", nil)
		r.Header = header
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	authorized := func() http.Header { return http.Header{"Authorization": {"Bearer fixture"}} }
	first := perform("GET", authorized(), info.ID)
	if first.Code != 200 || first.Body.String() != "0123456789" || requests != 2 || f.storage.opens != 1 {
		t.Fatal(first.Code, first.Body.String(), requests, f.storage.opens)
	}
	if first.Header().Get("Content-Disposition") != "attachment; filename=private.txt" || first.Header().Get("Cache-Control") != "private, no-store" || first.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(first.Header())
	}
	etag := first.Header().Get("Etag")
	for _, tc := range []struct {
		method string
		header http.Header
		status int
		body   string
	}{
		{"HEAD", authorized(), 200, ""},
		{"GET", http.Header{"Authorization": {"Bearer fixture"}, "If-None-Match": {etag}}, 304, ""},
		{"GET", http.Header{"Authorization": {"Bearer fixture"}, "Range": {"bytes=3-5"}}, 206, "345"},
		{"GET", http.Header{"Authorization": {"Bearer fixture"}, "Range": {"bytes=99-"}}, 416, ""},
		{"GET", http.Header{"If-None-Match": {etag}}, 401, "Unauthorized\n"},
	} {
		w := perform(tc.method, tc.header, info.ID)
		if w.Code != tc.status || w.Body.String() != tc.body {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	before := f.storage.opens
	// Current owner authority is checked even with a matching private validator.
	if _, err := f.backend.Exec(context.Background(), `UPDATE files_document SET allowed=false WHERE tenant_key='one' AND document_number=1`); err != nil {
		t.Fatal(err)
	}
	w := perform("GET", http.Header{"Authorization": {"Bearer fixture"}, "If-None-Match": {etag}}, info.ID)
	if w.Code != 403 || f.storage.opens != before || w.Header().Get("Etag") != "" {
		t.Fatal(w.Code, f.storage.opens, w.Header())
	}
	if _, err := f.backend.Exec(context.Background(), `UPDATE files_document SET allowed=true WHERE tenant_key='one' AND document_number=1`); err != nil {
		t.Fatal(err)
	}
	// Replacement retires exactly the old metadata; old bytes are retained but
	// are no longer downloadable through the previous ID/current owner link.
	newInfo, err := s.StoreValidated(context.Background(), nativeFileInput(t), strings.NewReader("replacement"))
	if err != nil {
		t.Fatal(err)
	}
	w = perform("HEAD", authorized(), info.ID)
	if w.Code != 404 || f.storage.opens != before {
		t.Fatal(w.Code, f.storage.opens)
	}
	w = perform("GET", authorized(), newInfo.ID)
	if w.Code != 200 || w.Body.String() != "replacement" {
		t.Fatal(w.Code, w.Body.String())
	}
	// Privileged drift cannot convert a valid owner_ref into current ownership.
	if _, err := f.backend.Exec(context.Background(), `UPDATE files_document SET private_asset='' WHERE tenant_key='one' AND document_number=1`); err != nil {
		t.Fatal(err)
	}
	before = f.storage.opens
	w = perform("GET", authorized(), newInfo.ID)
	if w.Code != 404 || f.storage.opens != before {
		t.Fatal(w.Code, f.storage.opens)
	}
}

type nativeDownloadStorage struct {
	files.Storage
	short  atomic.Bool
	closed atomic.Int32
}

func (s *nativeDownloadStorage) Open(ctx context.Context, key string) (files.Reader, error) {
	r, err := s.Storage.Open(ctx, key)
	if err != nil {
		return r, err
	}
	return &nativeDownloadReader{Reader: r, short: s.short.Load(), closed: &s.closed}, nil
}

type nativeDownloadReader struct {
	files.Reader
	short  bool
	reads  int
	closed *atomic.Int32
}

func (r *nativeDownloadReader) Read(p []byte) (int, error) {
	r.reads++
	if r.short && r.reads > 1 {
		return 0, io.EOF
	}
	return r.Reader.Read(p)
}
func (r *nativeDownloadReader) Close() error { r.closed.Add(1); return r.Reader.Close() }

func TestFilesDownloadNativeServerStreamingAndAbort(t *testing.T) {
	f := newNativeFileFixture(t)
	storage := &nativeDownloadStorage{Storage: f.storage.Storage}
	f.config.Storage = storage
	s := f.service(t)
	info, err := s.StoreValidated(context.Background(), nativeFileInput(t), strings.NewReader(strings.Repeat("private", 20000)))
	if err != nil {
		t.Fatal(err)
	}
	router := nativeDownloadRouter(t, s, func(r *http.Request) error {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			return auth.ErrUnauthenticated
		}
		return nil
	})
	var direct atomic.Int32
	server, err := ghttp.NewServer(ghttp.ServerConfig{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A buffering TimeoutHandler does not expose the transport deadline
			// controller. This checks the actual existing Streaming bypass without
			// writing a header or bypassing the download's authentication.
			if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				http.Error(w, "not a direct stream", 500)
				return
			}
			direct.Add(1)
			router.ServeHTTP(w, r)
		}),
		HandlerTimeout: time.Nanosecond,
		Streaming:      func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/private/files/") },
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("owned server did not stop")
		}
	})
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	perform := func() (*http.Response, error) {
		r, err := http.NewRequest("GET", "http://"+listener.Addr().String()+"/private/files/"+info.ID+"/", nil)
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer fixture")
		return client.Do(r)
	}
	response, err := perform()
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != 200 || len(data) != 140000 || direct.Load() != 1 {
		t.Fatal(response.StatusCode, len(data), readErr, closeErr, direct.Load())
	}
	storage.short.Store(true)
	response, err = perform()
	if err != nil {
		t.Fatal(err)
	}
	data, readErr = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || !errors.Is(readErr, io.ErrUnexpectedEOF) || len(data) != 64<<10 || strings.Contains(string(data), "Service Unavailable") || direct.Load() != 2 {
		t.Fatal(response.StatusCode, len(data), readErr, direct.Load())
	}
	// Service.Open completed before streaming. Each owned reader was closed
	// even when the transport was aborted after its first authorized chunk.
	if storage.closed.Load() != 2 {
		t.Fatal("reader ownership", storage.closed.Load())
	}
}
