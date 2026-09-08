package static

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Newton-School/gogo/core/app"
)

func testCollector(t *testing.T, files fstest.MapFS) (*Collector, string) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "collected")
	c, err := New(Config{Sources: []Source{{Owner: "project", FS: files}}, Destination: destination, BaseURL: "/static/"})
	if err != nil {
		t.Fatal(err)
	}
	return c, destination
}
func requireCollect(t *testing.T, c *Collector) Report {
	t.Helper()
	r, err := c.Collect(context.Background(), CollectOptions{})
	if err != nil || !r.Published || r.Manifest == nil {
		t.Fatal(r, err)
	}
	return r
}

func TestStaticDiscoveryPrecedenceAppRegistrationAndNoWrites(t *testing.T) {
	registry := &app.Registry{}
	first := fstest.MapFS{"shared.txt": {Data: []byte("first")}, "app/only.txt": {Data: []byte("app")}}
	if err := Register(registry, "first", Source{FS: first}); err != nil {
		t.Fatal(err)
	}
	if err := Register(registry, "second", Source{FS: fstest.MapFS{"shared.txt": {Data: []byte("second")}}}); err != nil {
		t.Fatal(err)
	}
	sources, err := AppSources(registry, []app.Config{{Name: "second", Label: "second", Requires: []string{"first"}}, {Name: "first", Label: "first"}})
	if err != nil || len(sources) != 2 || sources[0].Owner != "first" {
		t.Fatal(sources, err)
	}
	project := Source{Owner: "project", FS: fstest.MapFS{"shared.txt": {Data: []byte("project")}, ".env": {Data: []byte("not public")}}}
	destination := filepath.Join(t.TempDir(), "output")
	c, err := New(Config{Sources: append([]Source{project}, sources...), Destination: destination, BaseURL: "/static/"})
	if err != nil {
		t.Fatal(err)
	}
	sources[0].Owner = "replaced"
	matches, err := c.Find(context.Background(), "shared.txt")
	if err != nil || len(matches) != 3 || !matches[0].Selected || matches[1].Selected || matches[2].Selected || matches[1].Owner != "first" {
		t.Fatal(matches, err)
	}
	if _, err = os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("Find touched destination", err)
	}
	if _, err = c.Find(context.Background(), ".env"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = c.Find(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestStaticCollectDryRunDeterminismAndOldAssetsRetained(t *testing.T) {
	files := fstest.MapFS{"css/app.css": {Data: []byte(`/* url(missing.png) */ body{background:URL('../images/a.png?v=1#icon')} @import "base.css" layer(theme) screen;`)}, "css/base.css": {Data: []byte("body{color:blue}")}, "images/a.png": {Data: []byte("image")}}
	c, destination := testCollector(t, files)
	dry, err := c.Collect(context.Background(), CollectOptions{DryRun: true})
	if err != nil || dry.Published || !dry.DryRun {
		t.Fatal(dry, err)
	}
	if _, err = os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("dry-run wrote destination", err)
	}
	first := requireCollect(t, c)
	if !bytes.Equal(dry.Manifest.JSON(), first.Manifest.JSON()) {
		t.Fatal("dry-run differs from collection")
	}
	second := requireCollect(t, c)
	if !bytes.Equal(first.Manifest.JSON(), second.Manifest.JSON()) {
		t.Fatal("unchanged collection is not deterministic")
	}
	css := first.Manifest.assets["css/app.css"]
	contents, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(css.Versioned)))
	if err != nil {
		t.Fatal(err)
	}
	imageName := first.Manifest.assets["images/a.png"].Versioned
	if !strings.Contains(string(contents), "../"+imageName+"?v=1#icon") || !strings.Contains(string(contents), "/* url(missing.png) */") || !strings.Contains(string(contents), "layer(theme) screen;") {
		t.Fatal(string(contents))
	}
	files["images/a.png"] = &fstest.MapFile{Data: []byte("new image")}
	third := requireCollect(t, c)
	if third.Manifest.assets["css/app.css"].SHA256 == css.SHA256 {
		t.Fatal("dependency change did not version parent CSS")
	}
	if _, err = os.Stat(filepath.Join(destination, filepath.FromSlash(css.Versioned))); err != nil {
		t.Fatal("old client asset removed", err)
	}
	loaded, err := LoadManifest(context.Background(), destination, "https://cdn.example/static/")
	if err != nil {
		t.Fatal(err)
	}
	url, err := loaded.URL("css/app.css")
	if err != nil || !strings.HasPrefix(url, "https://cdn.example/static/css/") {
		t.Fatal(url, err)
	}
	assets := loaded.Assets()
	assets[0].Versioned = "changed"
	encoded := loaded.JSON()
	encoded[0] = '!'
	if bytes.Equal(encoded, loaded.JSON()) || reflect.DeepEqual(assets, loaded.Assets()) {
		t.Fatal("manifest delivery aliases internal state")
	}
}

func TestStaticCollectFailureKeepsPreviousManifest(t *testing.T) {
	for _, mode := range []string{"missing", "cycle", "query_self", "syntax", "unsupported", "limit"} {
		t.Run(mode, func(t *testing.T) {
			files := fstest.MapFS{"app.css": {Data: []byte("body{}")}, "asset.txt": {Data: []byte("old")}}
			c, destination := testCollector(t, files)
			before := requireCollect(t, c).Manifest.JSON()
			source := `body{background:url(missing.png)}`
			switch mode {
			case "cycle":
				source = `@import "app.css";`
			case "query_self":
				source = `body{background:url(?v=2)}`
			case "syntax":
				source = `body{background:url("bad)}`
			case "unsupported":
				source = `body{background:image-set("asset.txt" 1x)}`
			case "limit":
				source = strings.Repeat("x", 20)
				c.config.Limits.MaxCSSBytes = 10
			}
			files["app.css"] = &fstest.MapFile{Data: []byte(source)}
			report, err := c.Collect(context.Background(), CollectOptions{})
			if err == nil || report.Published || report.Manifest != nil {
				t.Fatal(report, err)
			}
			after, err := os.ReadFile(filepath.Join(destination, ManifestName))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("active manifest changed", err)
			}
		})
	}
}

func TestStaticPathConfinementAndRootSeparation(t *testing.T) {
	for _, name := range []string{"", ".", "../a", "a/../b", "/a", "a/", "a//b", `a\b`, "a?x", "a#x", "a\x00b", "a. ", "a/.."} {
		if validPath(name) {
			t.Errorf("invalid path accepted %q", name)
		}
	}
	root := t.TempDir()
	source := filepath.Join(root, "public")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Sources: []Source{{Owner: "project", Directory: source}}, Destination: filepath.Join(root, "output"), BaseURL: "/static/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Collect(context.Background(), CollectOptions{}); err == nil {
		t.Fatal("symlink published")
	}
	c.config.Destination = filepath.Join(source, "collected")
	if _, err = c.Collect(context.Background(), CollectOptions{DryRun: true}); !errors.Is(err, ErrInvalid) {
		t.Fatal("nested destination accepted", err)
	}
	c.config.Destination = root
	if _, err = c.Collect(context.Background(), CollectOptions{DryRun: true}); !errors.Is(err, ErrInvalid) {
		t.Fatal("ancestor destination accepted", err)
	}
}

func TestStaticLocalConflictingHashedFileNeverOverwritten(t *testing.T) {
	c, destination := testCollector(t, fstest.MapFS{"asset.txt": {Data: []byte("public")}})
	dry, err := c.Collect(context.Background(), CollectOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(destination, dry.Manifest.assets["asset.txt"].Versioned)
	if err = os.WriteFile(name, []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	if report, err := c.Collect(context.Background(), CollectOptions{}); err == nil || report.Published {
		t.Fatal(report, err)
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != "different" {
		t.Fatal("existing file overwritten", err)
	}
	if _, err = os.Stat(filepath.Join(destination, ManifestName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("manifest published", err)
	}
}

func TestStaticConcurrentCollectorsPublishCompleteManifests(t *testing.T) {
	c, destination := testCollector(t, fstest.MapFS{"a.txt": {Data: []byte("a")}, "b.txt": {Data: []byte("b")}})
	var wait sync.WaitGroup
	failures := make(chan error, 4)
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := c.Collect(context.Background(), CollectOptions{})
			failures <- err
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	m, err := LoadManifest(context.Background(), destination, "/static/")
	if err != nil || len(m.Assets()) != 2 {
		t.Fatal(m, err)
	}
	for _, asset := range m.Assets() {
		data, err := os.ReadFile(filepath.Join(destination, asset.Versioned))
		if err != nil || digest(data) != asset.SHA256 {
			t.Fatal("partial manifest", asset, err)
		}
	}
}

type failingFS struct{ err error }

func (f failingFS) Open(string) (fs.File, error) { return nil, f.err }

type panicFS struct{}

func (panicFS) Open(string) (fs.File, error) { panic("private source path") }

type brokenContext struct{ context.Context }

func (brokenContext) Err() error { panic("private context") }

func TestStaticSourcesErrorsCancellationAndLimits(t *testing.T) {
	for _, filesystem := range []fs.FS{failingFS{io.ErrUnexpectedEOF}, panicFS{}} {
		c, err := New(Config{Sources: []Source{{Owner: "project", FS: filesystem}}, Destination: filepath.Join(t.TempDir(), "out"), BaseURL: "/static/"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.Collect(context.Background(), CollectOptions{}); !errors.Is(err, ErrSource) || strings.Contains(err.Error(), "private") {
			t.Fatal(err)
		}
	}
	c, _ := testCollector(t, fstest.MapFS{"a": {Data: []byte("12")}, "b": {Data: []byte("34")}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Collect(ctx, CollectOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.Find(brokenContext{context.Background()}, "a"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	for _, limits := range []Limits{{MaxEntries: 1}, {MaxFileBytes: 1}, {MaxTotalBytes: 3}} {
		copy := *c
		copy.config.Limits, _ = normalizeLimits(limits)
		if _, err := copy.Collect(context.Background(), CollectOptions{DryRun: true}); !errors.Is(err, ErrLimit) {
			t.Fatal(limits, err)
		}
	}
}
