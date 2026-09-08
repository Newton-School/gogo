package static

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Newton-School/gogo/core/templates"
)

type terminalDirectory struct {
	fs.ReadDirFile
	failure error
	closed  *int
}

func (f terminalDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	entries, err := f.ReadDirFile.ReadDir(n)
	if err == io.EOF {
		return entries, errors.Join(io.EOF, f.failure)
	}
	return entries, err
}
func (f terminalDirectory) Close() error { *f.closed++; return f.ReadDirFile.Close() }

type terminalDirectoryFS struct {
	fstest.MapFS
	failure error
	closed  *int
}

func (f terminalDirectoryFS) Open(name string) (fs.File, error) {
	file, err := f.MapFS.Open(name)
	if err != nil {
		return nil, err
	}
	if dir, ok := file.(fs.ReadDirFile); ok && name == "." {
		return terminalDirectory{dir, f.failure, f.closed}, nil
	}
	return file, nil
}

func TestStaticMixedDirectoryEOFNeverPublishesPartialDiscovery(t *testing.T) {
	files := fstest.MapFS{"asset.txt": {Data: []byte("one")}}
	c, destination := testCollector(t, files)
	before := requireCollect(t, c).Manifest.JSON()
	failure := errors.New("private directory failure")
	closed := 0
	c.config.Sources[0].FS = terminalDirectoryFS{files, failure, &closed}
	r, err := c.Collect(context.Background(), CollectOptions{})
	if !errors.Is(err, failure) || !errors.Is(err, ErrSource) || r.Manifest != nil || r.Published || closed != 1 || strings.Contains(fmt.Sprint(err), "private") {
		t.Fatal(r, err, closed)
	}
	after, e := os.ReadFile(filepath.Join(destination, ManifestName))
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("old manifest changed", e)
	}
}

func TestStaticManifestStrictSchemaAndSafeErrors(t *testing.T) {
	c, _ := testCollector(t, fstest.MapFS{"one.txt": {Data: []byte("one")}})
	r, err := c.Collect(context.Background(), CollectOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	valid := string(r.Manifest.JSON())
	for _, input := range []string{
		`{}`, `{"version":2,"assets":[]}`, `{"version":1,"assets":null}`, `{"version":1,"version":1,"assets":[]}`,
		`{"version":1,"assets":[],"private":true}`, valid + `{}`, strings.Replace(valid, `"size":3`, `"size":-1`, 1),
		strings.Replace(valid, `"one.txt"`, `"../one.txt"`, 1), strings.Replace(valid, digest([]byte("one")), strings.Repeat("A", 64), -1),
	} {
		if m, err := ReadManifest(context.Background(), strings.NewReader(input), "/static/"); m != nil || err == nil {
			t.Fatal(input, m, err)
		}
	}
	if m, err := ReadManifest(context.Background(), strings.NewReader(strings.Repeat(" ", maxManifestBytes+1)), "/static/"); m != nil || !errors.Is(err, ErrLimit) {
		t.Fatal(m, err)
	}
	if m, err := LoadManifest(context.Background(), filepath.Join(t.TempDir(), "missing"), "/static/"); m != nil || !errors.Is(err, ErrSource) || strings.Contains(fmt.Sprintf("%+v", err), "missing") {
		t.Fatal(m, err)
	}
	private := &os.PathError{Op: "close", Path: "private-deployment-path", Err: errors.New("private provider")}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, fail(ErrSource, errors.Join(ErrInvalid, private))), "private") {
			t.Fatal("cleanup error formatting exposed path")
		}
	}
}

func TestStaticTemplateTagsFrozenEscapedAndStrict(t *testing.T) {
	c, _ := testCollector(t, fstest.MapFS{"img&assets/a.svg": {Data: []byte("one")}})
	r, err := c.Collect(context.Background(), CollectOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	want, err := r.Manifest.URL("img&assets/a.svg")
	if err != nil {
		t.Fatal(err)
	}
	engine := templates.New(templates.Config{Strict: true, Tags: r.Manifest.Tags(), Libraries: []string{"static"}})
	*r.Manifest = Manifest{}
	out, err := engine.RenderString(context.Background(), `{% load static %}<img src="{% static name %}">{% static name as asset %}<a href="{{ asset }}">{% get_static_prefix %}</a>`, templates.Context{"name": "img&assets/a.svg"})
	if err != nil || strings.Count(out, strings.ReplaceAll(want, "&", "&amp;")) != 2 || strings.Contains(out, "img&assets") {
		t.Fatal(out, err)
	}
	if out, err = engine.RenderString(context.Background(), `{% load static %}{% static "missing" %}`, nil); err == nil || out != "" {
		t.Fatal(out, err)
	}
}

type staticContextMutation struct {
	context.Context
	once   sync.Once
	mutate func()
}

func (c *staticContextMutation) Err() error { c.once.Do(c.mutate); return c.Context.Err() }

func TestStaticDevHandlerConditionalsAndRequestSnapshot(t *testing.T) {
	c, destination := testCollector(t, fstest.MapFS{"asset.txt": {Data: []byte("content")}})
	if _, err := c.DevHandler(false); err != ErrNotDebug {
		t.Fatal(err)
	}
	h, err := c.DevHandler(true)
	if err != nil {
		t.Fatal(err)
	}
	etag := `"` + digest([]byte("content")) + `"`
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, matched := range []bool{false, true} {
			r := httptest.NewRequest(method, "http://example.test/static/asset.txt", nil)
			if matched {
				r.Header["If-None-Match"] = []string{`"other"`, etag}
			}
			ctx := &staticContextMutation{Context: r.Context(), mutate: func() {
				r.Method = http.MethodPost
				r.URL.Path = "/private"
				if matched {
					r.Header["If-None-Match"][1] = `"changed"`
				}
			}}
			r = r.WithContext(ctx)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := http.StatusOK
			if matched {
				want = http.StatusNotModified
			}
			if w.Code != want || (method == http.MethodHead || matched) && w.Body.Len() != 0 || method == http.MethodGet && !matched && w.Body.String() != "content" {
				t.Fatal(method, matched, w.Code, w.Body.String())
			}
		}
	}
	for _, test := range []struct {
		method, path string
		status       int
	}{{"POST", "/static/asset.txt", 405}, {"GET", "/static/missing", 404}, {"GET", "/static/", 404}, {"GET", "/static/.env", 404}, {"GET", "/elsewhere", 404}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(test.method, "http://example.test"+test.path, nil))
		if w.Code != test.status {
			t.Fatal(test, w.Code)
		}
	}
	if _, err := os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("development serving created output", err)
	}
}

type shortStaticWriter struct {
	header http.Header
	calls  int
}

func (w *shortStaticWriter) Header() http.Header         { return w.header }
func (*shortStaticWriter) WriteHeader(int)               {}
func (w *shortStaticWriter) Write(p []byte) (int, error) { w.calls++; return len(p) - 1, nil }
func TestStaticDevHandlerAbortsIncompleteTransfer(t *testing.T) {
	c, _ := testCollector(t, fstest.MapFS{"asset.txt": {Data: []byte("content")}})
	h, err := c.DevHandler(true)
	if err != nil {
		t.Fatal(err)
	}
	w := &shortStaticWriter{header: http.Header{}}
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler || w.calls != 1 {
			t.Fatal(recovered, w.calls)
		}
	}()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://example.test/static/asset.txt", nil))
	t.Fatal("short transfer completed normally")
}

type closeFailureFile struct {
	fs.File
	failure error
	closed  *int
}

func (f closeFailureFile) Close() error { *f.closed++; return errors.Join(f.File.Close(), f.failure) }

type closeFailureFS struct {
	fstest.MapFS
	failure error
	closed  *int
}

func (f closeFailureFS) Open(name string) (fs.File, error) {
	file, err := f.MapFS.Open(name)
	if err != nil || name == "." {
		return file, err
	}
	return closeFailureFile{file, f.failure, f.closed}, nil
}
func TestStaticAssetCompletionFailureAndOperationSnapshot(t *testing.T) {
	files := fstest.MapFS{"one.txt": {Data: []byte("one")}}
	c, destination := testCollector(t, files)
	failure := errors.New("private close failure")
	closed := 0
	c.config.Sources[0].FS = closeFailureFS{files, failure, &closed}
	report, err := c.Collect(context.Background(), CollectOptions{})
	if report.Manifest != nil || report.Published || !errors.Is(err, failure) || closed != 1 || strings.Contains(fmt.Sprint(err), "private") {
		t.Fatal(report, err, closed)
	}
	if _, err := os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("completion failure wrote destination", err)
	}

	original, _ := testCollector(t, files)
	replacement, _ := testCollector(t, fstest.MapFS{"other.txt": {Data: []byte("other")}})
	ctx := &staticContextMutation{Context: context.Background(), mutate: func() { *original = *replacement }}
	report, err = original.Collect(ctx, CollectOptions{DryRun: true})
	if err != nil || len(report.Manifest.Assets()) != 1 || report.Manifest.Assets()[0].Path != "one.txt" {
		t.Fatal(report, err)
	}
	next, err := original.Collect(context.Background(), CollectOptions{DryRun: true})
	if err != nil || next.Manifest.Assets()[0].Path != "other.txt" {
		t.Fatal(next, err)
	}
}
