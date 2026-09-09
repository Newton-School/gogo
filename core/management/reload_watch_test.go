package management

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reloadTestFile(t *testing.T, directory, name, data string) {
	t.Helper()
	file := filepath.Join(directory, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func reloadTestWatcher(t *testing.T, directory string, extra, excluded []string) *reloadWatcher {
	t.Helper()
	w, err := newReloadWatcher(directory, extra, excluded)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.root.Close(); err != nil {
			t.Error(err)
		}
	})
	return w
}

func TestReloadWatcherContentSelectionAndRootOnlyExclusions(t *testing.T) {
	directory := t.TempDir()
	for name, data := range map[string]string{
		"manage.go": "original", "go.mod": "module example.test/project", ".env": "GOGO_DEBUG=false",
		"apps/media/views.go": "media original", "apps/build/templates/page.html": "page original",
		"assets/icon.svg": "icon original", "uploads/private.go": "secret", "private/files/value.go": "private",
		"bin/main.go": "binary", ".git/config": "git", "apps/demo/temp.go~": "editor", "notes.txt": "notes",
		"apps/build/templates/.env": "template private", "assets/.env": "asset private",
	} {
		reloadTestFile(t, directory, name, data)
	}
	w := reloadTestWatcher(t, directory, []string{"assets"}, []string{"private/files"})
	snapshot := func() [32]byte {
		t.Helper()
		sum, err := w.snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}
	last := snapshot()
	if repeat := snapshot(); repeat != last {
		t.Fatal("unchanged source digest is unstable")
	}
	for _, ignored := range []string{"uploads/private.go", "private/files/value.go", "bin/main.go", ".git/config", "apps/demo/temp.go~", "notes.txt", "apps/build/templates/.env", "assets/.env"} {
		reloadTestFile(t, directory, ignored, "changed")
		if got := snapshot(); got != last {
			t.Fatalf("ignored source %q changed fingerprint", ignored)
		}
	}
	for _, selected := range []string{"manage.go", "go.mod", ".env", "apps/media/views.go", "apps/build/templates/page.html", "assets/icon.svg"} {
		file := filepath.Join(directory, selected)
		old, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		reloadTestFile(t, directory, selected, "changed content")
		if err := os.Chtimes(file, old.ModTime(), old.ModTime()); err != nil {
			t.Fatal(err)
		}
		got := snapshot()
		if got == last {
			t.Fatalf("selected source %q was invisible", selected)
		}
		last = got
	}
	if err := os.Remove(filepath.Join(directory, "apps/media/views.go")); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(); got == last {
		t.Fatal("source removal invisible")
	}
}

func TestReloadWatcherSymlinksAndOutputContainment(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "project")
	reloadTestFile(t, directory, "manage.go", "source")
	reloadTestFile(t, directory, "private/owned.go", "private")
	outside := t.TempDir()
	reloadTestFile(t, outside, "secret.go", "secret")
	if err := os.Symlink(outside, filepath.Join(directory, "linked")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := os.Symlink(filepath.Join(directory, "private"), filepath.Join(parent, "upload-alias")); err != nil {
		t.Fatal(err)
	}
	w := reloadTestWatcher(t, directory, nil, []string{filepath.Join(parent, "upload-alias")})
	before, err := w.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reloadTestFile(t, outside, "secret.go", "changed outside")
	reloadTestFile(t, directory, "private/owned.go", "changed private")
	after, err := w.snapshot(context.Background())
	if err != nil || after != before {
		t.Fatal("symlink/excluded data affected snapshot", err)
	}
	for _, output := range []string{directory, parent, filepath.Dir(parent)} {
		if candidate, e := newReloadWatcher(directory, nil, []string{output}); e == nil {
			_ = candidate.root.Close()
			t.Fatalf("output ancestor %q accepted", output)
		}
	}
	if candidate, e := newReloadWatcher(directory, []string{"linked"}, nil); e == nil {
		_ = candidate.root.Close()
		t.Fatal("explicit symlink watch accepted")
	}
	if err := os.Symlink(parent, filepath.Join(parent, "parent-alias")); err != nil {
		t.Fatal(err)
	}
	if candidate, e := newReloadWatcher(directory, nil, []string{filepath.Join(parent, "parent-alias")}); e == nil {
		_ = candidate.root.Close()
		t.Fatal("symlink output ancestor accepted")
	}
}

func TestReloadWatcherBoundsCancellationAndDirectoryIdentity(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "project")
	reloadTestFile(t, directory, "manage.go", "source")
	w := reloadTestWatcher(t, directory, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.snapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	file, err := os.OpenFile(filepath.Join(directory, "huge.go"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(reloadMaxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err = w.snapshot(context.Background()); err == nil {
		t.Fatal("oversized file accepted")
	}
	if err = os.Remove(filepath.Join(directory, "huge.go")); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(directory, directory+"-old"); err != nil {
		t.Fatal(err)
	}
	reloadTestFile(t, directory, "manage.go", "replacement")
	if _, err = w.snapshot(context.Background()); err == nil {
		t.Fatal("retargeted project directory accepted")
	}
	for _, name := range []string{"", ".", "../escape", "/absolute", "a//b", "a/../b", "a/", "a\\b", "a\x00b", strings.Repeat("x", 1025)} {
		if reloadRelative(name) {
			t.Fatalf("noncanonical path %q accepted", name)
		}
	}
}
