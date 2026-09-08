package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef"

func newTestLocal(t *testing.T, maximum int64) (*Local, string) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("local provider supports Linux and macOS")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	l, err := NewLocal(LocalConfig{Directory: dir, MaxBytes: maximum})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	return l, dir
}

func assertNoStaging(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".gogo-") {
			t.Errorf("staging entry leaked: %q", e.Name())
		}
	}
}

func TestLocalRoundTripMetadataAndPrivateURL(t *testing.T) {
	l, directory := newTestLocal(t, 0)
	data := bytes.Repeat([]byte("bounded blob\x00"), 100000)
	ctx := context.Background()
	result, err := l.Save(ctx, testKey, bytes.NewReader(data))
	digest := sha256.Sum256(data)
	if err != nil || !result.Published || result.Key != testKey || result.Bytes != int64(len(data)) || result.SHA256 != hex.EncodeToString(digest[:]) || result.ModifiedTime.IsZero() {
		t.Fatalf("save = %+v, %v", result, err)
	}
	assertNoStaging(t, directory)
	info, err := os.Stat(filepath.Join(directory, testKey))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private mode: %v, %v", info, err)
	}
	reader, err := l.Open(ctx, testKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read length=%d error=%v", len(got), err)
	}
	if at, err := reader.Seek(8, io.SeekStart); at != 8 || err != nil {
		t.Fatalf("seek=%d,%v", at, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if exists, err := l.Exists(ctx, testKey); !exists || err != nil {
		t.Fatalf("exists=%v,%v", exists, err)
	}
	if size, err := l.Size(ctx, testKey); size != result.Bytes || err != nil {
		t.Fatalf("size=%d,%v", size, err)
	}
	if modified, err := l.ModifiedTime(ctx, testKey); !modified.Equal(result.ModifiedTime) || err != nil {
		t.Fatalf("modified=%v,%v", modified, err)
	}
	if url, err := l.URL(ctx, testKey); url != "" || !errors.Is(err, ErrStorageCapabilityUnavailable) {
		t.Fatalf("url=%q,%v", url, err)
	}
	if err := l.Delete(ctx, testKey); err != nil {
		t.Fatal(err)
	}
	if err := l.Delete(ctx, testKey); err != nil {
		t.Fatal(err)
	}
	if exists, err := l.Exists(ctx, testKey); exists || err != nil {
		t.Fatalf("missing exists=%v,%v", exists, err)
	}
	if reader, err := l.Open(ctx, testKey); reader != nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing open=%v,%v", reader, err)
	}
	if size, err := l.Size(ctx, testKey); size != 0 || !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing size=%d,%v", size, err)
	}
}

func TestLocalKeyAndConfigurationBoundaries(t *testing.T) {
	l, directory := newTestLocal(t, 8)
	ctx := context.Background()
	for _, key := range []string{"", ".", "..", "../" + testKey, testKey + "/", "/" + testKey, strings.ToUpper(testKey), testKey + ".txt", testKey + "\x00", strings.Repeat("g", 32), testKey[:31], strings.Repeat("a", 33)} {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			called := false
			source := readerFunc(func([]byte) (int, error) { called = true; return 0, io.EOF })
			result, err := l.Save(ctx, key, source)
			if result != (SaveResult{}) || !errors.Is(err, ErrInvalidKey) || called {
				t.Fatalf("save=%+v,%v reader=%v", result, err, called)
			}
			if r, e := l.Open(ctx, key); r != nil || !errors.Is(e, ErrInvalidKey) {
				t.Fatalf("open=%v,%v", r, e)
			}
			if e := l.Delete(ctx, key); !errors.Is(e, ErrInvalidKey) {
				t.Fatal(e)
			}
			if v, e := l.Exists(ctx, key); v || !errors.Is(e, ErrInvalidKey) {
				t.Fatalf("exists=%v,%v", v, e)
			}
			if v, e := l.URL(ctx, key); v != "" || !errors.Is(e, ErrInvalidKey) {
				t.Fatalf("url=%q,%v", v, e)
			}
		})
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v,%v", entries, err)
	}
	for n := 0; n < 32; n++ {
		key, err := NewKey()
		if err != nil || !validKey(key) {
			t.Fatalf("key=%q,%v", key, err)
		}
	}
	if _, err := NewLocal(LocalConfig{}); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	if _, err := NewLocal(LocalConfig{Directory: directory, MaxBytes: -1}); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	public := t.TempDir()
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocal(LocalConfig{Directory: public}); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	if _, err := NewLocal(LocalConfig{Directory: filepath.Join(directory, "missing")}); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	var nilLocal *Local
	if _, err := nilLocal.Size(ctx, testKey); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	if _, err := (&Local{}).Size(ctx, testKey); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	if _, err := l.Save(nil, testKey, strings.NewReader("a")); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	var nilContext *storageMutationContext
	if _, err := l.Save(nilContext, testKey, strings.NewReader("a")); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	var nilReader *bytes.Reader
	if _, err := l.Save(ctx, testKey, nilReader); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestLocalStreamingFailuresDoNotPublish(t *testing.T) {
	failure := errors.New("reader secret detail")
	for _, test := range []struct {
		name   string
		source io.Reader
		want   error
	}{
		{"over limit", strings.NewReader("123456789"), ErrLimit},
		{"reader error", readerFunc(func(p []byte) (int, error) { p[0] = 'x'; return 1, failure }), failure},
		{"negative count", readerFunc(func([]byte) (int, error) { return -1, nil }), io.ErrShortBuffer},
		{"oversized count", readerFunc(func(p []byte) (int, error) { return len(p) + 1, nil }), io.ErrShortBuffer},
		{"no progress", readerFunc(func([]byte) (int, error) { return 0, nil }), io.ErrNoProgress},
	} {
		t.Run(test.name, func(t *testing.T) {
			l, dir := newTestLocal(t, 8)
			result, err := l.Save(context.Background(), testKey, test.source)
			if result != (SaveResult{}) || !errors.Is(err, test.want) {
				t.Fatalf("save=%+v,%v", result, err)
			}
			if strings.Contains(fmt.Sprintf("%+v", err), "secret") {
				t.Fatal("reader message leaked")
			}
			if entries, e := os.ReadDir(dir); e != nil || len(entries) != 0 {
				t.Fatalf("leaked=%v,%v", entries, e)
			}
		})
	}
	for _, body := range []string{"", "12345678"} {
		t.Run(fmt.Sprintf("accepted%d", len(body)), func(t *testing.T) {
			l, _ := newTestLocal(t, 8)
			result, err := l.Save(context.Background(), testKey, strings.NewReader(body))
			if err != nil || !result.Published || result.Bytes != int64(len(body)) {
				t.Fatalf("save=%+v,%v", result, err)
			}
		})
	}
}

func TestLocalSaveStreamsAtMostOneByteBeyondLimit(t *testing.T) {
	const maximum = 3*(64<<10) + 17
	l, dir := newTestLocal(t, maximum)
	read := 0
	result, err := l.Save(context.Background(), testKey, readerFunc(func(p []byte) (int, error) {
		if len(p) > 64<<10 {
			t.Fatalf("unbounded buffer: %d", len(p))
		}
		for i := range p {
			p[i] = 'x'
		}
		read += len(p)
		return len(p), nil
	}))
	if result != (SaveResult{}) || !errors.Is(err, ErrLimit) || read != maximum+1 {
		t.Fatalf("save=%+v,%v read=%d", result, err, read)
	}
	if entries, e := os.ReadDir(dir); e != nil || len(entries) != 0 {
		t.Fatalf("leaked=%v,%v", entries, e)
	}
	defaults, _ := newTestLocal(t, 0)
	if defaults.state.maximum != DefaultMaxBytes {
		t.Fatal("default changed")
	}
}

func TestLocalSaveCancellationAndPanicCleanup(t *testing.T) {
	for _, mode := range []string{"before", "during", "mixed", "panic"} {
		t.Run(mode, func(t *testing.T) {
			l, dir := newTestLocal(t, 8)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("source failure")
			if mode == "before" {
				cancel()
			}
			called := false
			source := readerFunc(func(p []byte) (int, error) {
				called = true
				if mode == "panic" {
					panic(failure)
				}
				p[0] = 'x'
				cancel()
				if mode == "mixed" {
					return 1, failure
				}
				return 1, io.EOF
			})
			func() {
				result, err := l.Save(ctx, testKey, source)
				want := context.Canceled
				if mode == "panic" {
					want = ErrUnavailable
				}
				if result != (SaveResult{}) || !errors.Is(err, want) {
					t.Fatalf("save=%+v,%v", result, err)
				}
				if mode == "mixed" && !errors.Is(err, failure) {
					t.Fatal("lost reader failure")
				}
			}()
			if mode == "before" && called {
				t.Fatal("called reader after cancellation")
			}
			if entries, e := os.ReadDir(dir); e != nil || len(entries) != 0 {
				t.Fatalf("leaked=%v,%v", entries, e)
			}
		})
	}
}

type storageMutationContext struct {
	context.Context
	once   sync.Once
	mutate func()
}

func (c *storageMutationContext) Err() error { c.once.Do(c.mutate); return c.Context.Err() }

func TestLocalSnapshotsBeforeCallbacksAndClose(t *testing.T) {
	original, originalDir := newTestLocal(t, 8)
	replacement, replacementDir := newTestLocal(t, 1)
	savedHandle := *original
	ctx := &storageMutationContext{Context: context.Background(), mutate: func() {
		if err := original.Close(); err != nil {
			t.Fatal(err)
		}
		*original = *replacement
	}}
	result, err := original.Save(ctx, testKey, strings.NewReader("original"))
	if err != nil || !result.Published {
		t.Fatalf("save=%+v,%v", result, err)
	}
	if data, e := os.ReadFile(filepath.Join(originalDir, testKey)); e != nil || string(data) != "original" {
		t.Fatalf("original=%q,%v", data, e)
	}
	if entries, e := os.ReadDir(replacementDir); e != nil || len(entries) != 0 {
		t.Fatalf("replacement=%v,%v", entries, e)
	}
	if _, e := savedHandle.Size(context.Background(), testKey); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
	assertNoStaging(t, originalDir)

	l, dir := newTestLocal(t, 8)
	called := false
	result, err = l.Save(context.Background(), testKey, readerFunc(func(p []byte) (int, error) {
		if called {
			t.Fatal("unexpected read")
		}
		called = true
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		copy(p, "blob")
		return 4, io.EOF
	}))
	if err != nil || !result.Published {
		t.Fatalf("inflight close=%+v,%v", result, err)
	}
	assertNoStaging(t, dir)
}

func TestLocalConcurrentCollisionNeverOverwrites(t *testing.T) {
	l, dir := newTestLocal(t, 128)
	var winners atomic.Int32
	var wait sync.WaitGroup
	for n := 0; n < 16; n++ {
		wait.Add(1)
		go func(n int) {
			defer wait.Done()
			body := fmt.Sprintf("writer-%d", n)
			result, err := l.Save(context.Background(), testKey, strings.NewReader(body))
			if err == nil {
				if !result.Published {
					t.Error("success not published")
				}
				winners.Add(1)
			} else if !errors.Is(err, ErrCollision) || result != (SaveResult{}) {
				t.Errorf("loser=%+v,%v", result, err)
			}
		}(n)
	}
	wait.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners=%d", winners.Load())
	}
	before, err := os.ReadFile(filepath.Join(dir, testKey))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := l.Save(context.Background(), testKey, strings.NewReader("replacement")); result != (SaveResult{}) || !errors.Is(err, ErrCollision) {
		t.Fatalf("overwrite=%+v,%v", result, err)
	}
	after, err := os.ReadFile(filepath.Join(dir, testKey))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing blob changed")
	}
	assertNoStaging(t, dir)
}

func TestLocalListIsBoundedSortedAndDoesNotExposeStaging(t *testing.T) {
	l, dir := newTestLocal(t, 8)
	for n := 20; n >= 1; n-- {
		key := fmt.Sprintf("%032x", n)
		if _, err := l.Save(context.Background(), key, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".gogo-unowned.tmp", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var all []string
	after := ""
	for {
		page, err := l.List(context.Background(), ListOptions{After: after, Limit: 3})
		if err != nil || len(page.Keys) > 3 {
			t.Fatalf("page=%+v,%v", page, err)
		}
		all = append(all, page.Keys...)
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if len(all) != 20 {
		t.Fatalf("count=%d", len(all))
	}
	for n, key := range all {
		if key != fmt.Sprintf("%032x", n+1) {
			t.Fatalf("key%d=%q", n, key)
		}
	}
	for _, limit := range []int{-1, 1001} {
		if p, e := l.List(context.Background(), ListOptions{Limit: limit}); !reflect.DeepEqual(p, Page{}) || !errors.Is(e, ErrLimit) {
			t.Fatalf("bad limit=%+v,%v", p, e)
		}
	}
	if p, e := l.List(context.Background(), ListOptions{After: "bad"}); !reflect.DeepEqual(p, Page{}) || !errors.Is(e, ErrInvalidKey) {
		t.Fatalf("bad cursor=%+v,%v", p, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, e := l.List(ctx, ListOptions{}); !reflect.DeepEqual(p, Page{}) || !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel=%+v,%v", p, e)
	}
	for _, name := range []string{".gogo-unowned.tmp", "unrelated.txt"} {
		if data, e := os.ReadFile(filepath.Join(dir, name)); e != nil || string(data) != "keep" {
			t.Fatalf("unowned=%q,%v", data, e)
		}
	}
}

func TestLocalRejectsLinksNonregularAndPublicObjects(t *testing.T) {
	for _, kind := range []string{"external symlink", "internal symlink", "directory", "public file"} {
		t.Run(kind, func(t *testing.T) {
			l, dir := newTestLocal(t, 8)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(dir, testKey)
			var err error
			switch kind {
			case "external symlink":
				err = os.Symlink(outside, destination)
			case "internal symlink":
				err = os.WriteFile(filepath.Join(dir, "internal"), []byte("secret"), 0600)
				if err == nil {
					err = os.Symlink("internal", destination)
				}
			case "directory":
				err = os.Mkdir(destination, 0700)
			case "public file":
				err = os.WriteFile(destination, []byte("secret"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if reader, e := l.Open(context.Background(), testKey); reader != nil || !errors.Is(e, ErrUnavailable) {
				t.Fatalf("open=%v,%v", reader, e)
			}
			if e := l.Delete(context.Background(), testKey); !errors.Is(e, ErrUnavailable) {
				t.Fatal(e)
			}
			if p, e := l.List(context.Background(), ListOptions{}); !reflect.DeepEqual(p, Page{}) || !errors.Is(e, ErrUnavailable) {
				t.Fatalf("list=%+v,%v", p, e)
			}
			if _, e := l.Save(context.Background(), testKey, strings.NewReader("x")); !errors.Is(e, ErrCollision) {
				t.Fatal(e)
			}
			if data, e := os.ReadFile(outside); e != nil || string(data) != "secret" {
				t.Fatal("outside modified")
			}
		})
	}
}

func TestLocalTracksRootRenameAndOpenReaderOwnership(t *testing.T) {
	l, dir := newTestLocal(t, 8)
	if _, err := l.Save(context.Background(), testKey, strings.NewReader("original")); err != nil {
		t.Fatal(err)
	}
	reader, err := l.Open(context.Background(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	renamed := filepath.Join(t.TempDir(), "renamed")
	if err := os.Rename(dir, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, testKey), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if size, err := l.Size(context.Background(), testKey); size != 8 || err != nil {
		t.Fatalf("size=%d,%v", size, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "original" {
		t.Fatalf("open descriptor=%q,%v", data, err)
	}
	if _, err := l.Open(context.Background(), testKey); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if data, e := os.ReadFile(filepath.Join(renamed, testKey)); e != nil || string(data) != "original" {
		t.Fatalf("renamed=%q,%v", data, e)
	}
}

func TestLocalFormattingAndErrorsDoNotRevealRoot(t *testing.T) {
	l, dir := newTestLocal(t, 8)
	if _, err := l.Save(context.Background(), testKey, strings.NewReader("blob")); err != nil {
		t.Fatal(err)
	}
	reader, err := l.Open(context.Background(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, value := range []any{l, reader, LocalConfig{Directory: dir}} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, value), dir) {
				t.Fatal("root leaked")
			}
		}
		if data, err := json.Marshal(value); err == nil || len(data) != 0 {
			t.Fatalf("marshal=%s,%v", data, err)
		}
	}
	_, err = l.Open(context.Background(), "00000000000000000000000000000000")
	if strings.Contains(fmt.Sprintf("%+v", err), dir) {
		t.Fatal("path error leaked")
	}
}

type invalidStorageContext struct{ context.Context }

func (invalidStorageContext) Err() error { return errors.New("private context detail") }

func TestLocalRejectsNonconformingContextErrors(t *testing.T) {
	l, dir := newTestLocal(t, 8)
	result, err := l.Save(invalidStorageContext{context.Background()}, testKey, strings.NewReader("blob"))
	if result != (SaveResult{}) || err != ErrUnavailable || strings.Contains(fmt.Sprint(err), "private") {
		t.Fatalf("save=%+v,%v", result, err)
	}
	if entries, e := os.ReadDir(dir); e != nil || len(entries) != 0 {
		t.Fatalf("leaked=%v,%v", entries, e)
	}
}

func TestLocalContextPanicsFailClosedAndCleanOwnedTemporary(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			l, dir := newTestLocal(t, 8)
			calls := 0
			ctx := &panicStorageContext{Context: context.Background(), fail: func() bool { calls++; return !late || calls >= 3 }}
			result, err := l.Save(ctx, testKey, strings.NewReader("blob"))
			if result != (SaveResult{}) || !errors.Is(err, ErrUnavailable) || strings.Contains(fmt.Sprint(err), "panic detail") {
				t.Fatalf("save=%+v,%v", result, err)
			}
			if entries, e := os.ReadDir(dir); e != nil || len(entries) != 0 {
				t.Fatalf("leaked=%v,%v", entries, e)
			}
		})
	}
}

type panicStorageContext struct {
	context.Context
	fail func() bool
}

func (c *panicStorageContext) Err() error {
	if c.fail() {
		panic("panic detail")
	}
	return nil
}
