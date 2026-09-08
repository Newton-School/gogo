package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"time"
)

// Local confines operations to an opened, private directory on Linux or macOS.
// Copies share Close admission state. Already-admitted operations and open
// readers own independent descriptors and may complete after Close.
type Local struct{ state *localState }
type localState struct {
	mu      sync.Mutex
	root    *os.Root
	maximum int64
	closed  bool
}

var _ Storage = (*Local)(nil)

// NewLocal opens an existing owner-private directory. It does not create, chmod,
// migrate, or sweep it. The directory and its ancestors must be administered by
// trusted parties; hostile filesystem writers and mount changes are not sandboxed.
func NewLocal(config LocalConfig) (*Local, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, ErrStorageCapabilityUnavailable
	}
	if config.Directory == "" || config.MaxBytes < 0 {
		return nil, ErrConfiguration
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = DefaultMaxBytes
	}
	root, err := os.OpenRoot(config.Directory)
	if err != nil {
		return nil, ErrConfiguration
	}
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		_ = root.Close()
		return nil, ErrConfiguration
	}
	return &Local{state: &localState{root: root, maximum: config.MaxBytes}}, nil
}

func (Local) String() string               { return "files.Local{root:redacted}" }
func (l Local) GoString() string           { return l.String() }
func (l Local) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, l.String()) }
func (Local) MarshalJSON() ([]byte, error) { return nil, ErrConfiguration }

// acquire snapshots the state and opens an operation-owned descriptor before
// invoking any caller Context or Reader method. No caller code runs under mu.
func (l *Local) acquire() (*os.Root, int64, error) {
	if l == nil || l.state == nil {
		return nil, 0, ErrConfiguration
	}
	s := l.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, ErrClosed
	}
	root, err := s.root.OpenRoot(".")
	if err != nil {
		return nil, 0, ErrUnavailable
	}
	return root, s.maximum, nil
}

func (l *Local) Close() error {
	if l == nil || l.state == nil {
		return ErrConfiguration
	}
	s := l.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.root.Close() != nil {
		return ErrUnavailable
	}
	return nil
}

func missingValue(v any) bool {
	if v == nil {
		return true
	}
	x := reflect.ValueOf(v)
	switch x.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return x.IsNil()
	}
	return false
}

func storageContext(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	if missingValue(ctx) {
		return ErrConfiguration
	}
	switch err := ctx.Err(); err {
	case nil, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrUnavailable
	}
}

func readSource(source io.Reader, p []byte) (n int, err error) {
	defer func() {
		if recover() != nil {
			n = 0
			err = ErrUnavailable
		}
	}()
	return source.Read(p)
}

func fsError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if errors.Is(err, os.ErrExist) {
		return ErrCollision
	}
	if errors.Is(err, os.ErrClosed) {
		return ErrClosed
	}
	return ErrUnavailable
}

func finishRoot(root *os.Root, err *error) {
	if root.Close() != nil {
		*err = errors.Join(*err, ErrUnavailable)
	}
}

// regularInfo rejects links and nonregular entries. Root confinement is retained
// even if a trusted administrator changes a name between this check and opening.
func regularInfo(root *os.Root, key string) (os.FileInfo, error) {
	info, err := root.Lstat(key)
	if err != nil {
		return nil, fsError(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrUnavailable
	}
	return info, nil
}

func (l *Local) Save(ctx context.Context, key string, source io.Reader) (result SaveResult, err error) {
	root, maximum, err := l.acquire()
	if err != nil {
		return SaveResult{}, err
	}
	defer finishRoot(root, &err)
	if !validKey(key) {
		return SaveResult{}, ErrInvalidKey
	}
	if missingValue(source) {
		return SaveResult{}, ErrConfiguration
	}
	if err = storageContext(ctx); err != nil {
		return SaveResult{}, err
	}
	identity, err := NewKey()
	if err != nil {
		return SaveResult{}, err
	}
	temporary := ".gogo-" + identity + ".tmp"
	out, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return SaveResult{}, ErrUnavailable
	}
	closed := false
	defer func() {
		if !closed && out.Close() != nil {
			err = errors.Join(err, ErrUnavailable)
		}
		if removeErr := root.Remove(temporary); removeErr != nil {
			err = errors.Join(err, ErrUnavailable)
		}
	}()
	digest := sha256.New()
	buffer := make([]byte, 64<<10)
	var size int64
	emptyReads := 0
	for {
		if err = storageContext(ctx); err != nil {
			return SaveResult{}, err
		}
		limit := len(buffer)
		if remaining := maximum - size; remaining < int64(limit) {
			limit = int(remaining) + 1
		}
		n, readErr := readSource(source, buffer[:limit])
		if err = storageContext(ctx); err != nil {
			if readErr != nil && readErr != io.EOF {
				err = errors.Join(err, &readError{cause: readErr})
			}
			return SaveResult{}, err
		}
		if n < 0 || n > limit {
			return SaveResult{}, &readError{cause: io.ErrShortBuffer}
		}
		if int64(n) > maximum-size {
			return SaveResult{}, ErrLimit
		}
		if readErr != nil && readErr != io.EOF {
			return SaveResult{}, &readError{cause: readErr}
		}
		if n != 0 {
			emptyReads = 0
			written, writeErr := out.Write(buffer[:n])
			if writeErr != nil {
				return SaveResult{}, ErrUnavailable
			}
			if written != n {
				return SaveResult{}, &readError{cause: io.ErrShortWrite}
			}
			_, _ = digest.Write(buffer[:n])
			size += int64(n)
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return SaveResult{}, &readError{cause: io.ErrNoProgress}
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if err = storageContext(ctx); err != nil {
		return SaveResult{}, err
	}
	if out.Sync() != nil {
		return SaveResult{}, ErrUnavailable
	}
	info, statErr := out.Stat()
	if statErr != nil {
		return SaveResult{}, ErrUnavailable
	}
	closeErr := out.Close()
	closed = true
	if closeErr != nil {
		return SaveResult{}, ErrUnavailable
	}
	if err = storageContext(ctx); err != nil {
		return SaveResult{}, err
	}
	if linkErr := root.Link(temporary, key); linkErr != nil {
		return SaveResult{}, fsError(linkErr)
	}
	// No callbacks or cancellation checks can turn this observed publication into
	// a zero result. Deferred cleanup errors retain the outcome and exact key.
	return SaveResult{Key: key, Bytes: size, SHA256: hex.EncodeToString(digest.Sum(nil)), ModifiedTime: info.ModTime().UTC(), Published: true}, nil
}

func (l *Local) Open(ctx context.Context, key string) (reader Reader, err error) {
	root, _, err := l.acquire()
	if err != nil {
		return nil, err
	}
	defer func() {
		finishRoot(root, &err)
		if err != nil && reader != nil {
			_ = reader.Close()
			reader = nil
		}
	}()
	if !validKey(key) {
		return nil, ErrInvalidKey
	}
	if err = storageContext(ctx); err != nil {
		return nil, err
	}
	before, err := regularInfo(root, key)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(key)
	if err != nil {
		return nil, fsError(err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	after, statErr := file.Stat()
	if statErr != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, ErrUnavailable
	}
	if err = storageContext(ctx); err != nil {
		return nil, err
	}
	keep = true
	return &localReader{file: file, ctx: ctx}, nil
}

type localReader struct {
	file *os.File
	ctx  context.Context
}

func (*localReader) String() string               { return "files.Reader{object:redacted}" }
func (r *localReader) GoString() string           { return r.String() }
func (r *localReader) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, r.String()) }
func (*localReader) MarshalJSON() ([]byte, error) { return nil, ErrConfiguration }

func (r *localReader) Read(p []byte) (int, error) {
	if err := storageContext(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.file.Read(p)
	if cancel := storageContext(r.ctx); cancel != nil {
		return n, cancel
	}
	if err == io.EOF {
		return n, err
	}
	return n, fsError(err)
}
func (r *localReader) Seek(offset int64, whence int) (int64, error) {
	if err := storageContext(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.file.Seek(offset, whence)
	if cancel := storageContext(r.ctx); cancel != nil {
		return n, cancel
	}
	return n, fsError(err)
}
func (r *localReader) Close() error { return fsError(r.file.Close()) }

func (l *Local) stat(ctx context.Context, key string) (info os.FileInfo, err error) {
	root, _, err := l.acquire()
	if err != nil {
		return nil, err
	}
	defer func() {
		finishRoot(root, &err)
		if err != nil {
			info = nil
		}
	}()
	if !validKey(key) {
		return nil, ErrInvalidKey
	}
	if err = storageContext(ctx); err != nil {
		return nil, err
	}
	info, err = regularInfo(root, key)
	if err != nil {
		return nil, err
	}
	if err = storageContext(ctx); err != nil {
		return nil, err
	}
	return info, nil
}

func (l *Local) Exists(ctx context.Context, key string) (bool, error) {
	_, err := l.stat(ctx, key)
	if err == ErrNotFound {
		return false, nil
	}
	return err == nil, err
}
func (l *Local) Size(ctx context.Context, key string) (int64, error) {
	info, err := l.stat(ctx, key)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
func (l *Local) ModifiedTime(ctx context.Context, key string) (time.Time, error) {
	info, err := l.stat(ctx, key)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime().UTC(), nil
}

// Delete is idempotent on an absent key. Nil means unlink was observed (or the
// object was already absent), not that a directory fsync/crash barrier completed.
// Once unlink succeeds, subsequent descriptor cleanup cannot report rollback.
func (l *Local) Delete(ctx context.Context, key string) (err error) {
	root, _, err := l.acquire()
	if err != nil {
		return err
	}
	applied := false
	defer func() {
		closeErr := root.Close()
		if !applied && closeErr != nil {
			err = errors.Join(err, ErrUnavailable)
		}
	}()
	if !validKey(key) {
		return ErrInvalidKey
	}
	if err = storageContext(ctx); err != nil {
		return err
	}
	if _, err = regularInfo(root, key); err != nil {
		if err == ErrNotFound {
			applied = true
			return nil
		}
		return err
	}
	if err = storageContext(ctx); err != nil {
		return err
	}
	if removeErr := root.Remove(key); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fsError(removeErr)
	}
	applied = true
	return nil
}

// URL never returns a local path or a public bypass. A future authorized file
// service can serve Open through its own current-grant download handler.
func (l *Local) URL(ctx context.Context, key string) (url string, err error) {
	root, _, err := l.acquire()
	if err != nil {
		return "", err
	}
	defer finishRoot(root, &err)
	if !validKey(key) {
		return "", ErrInvalidKey
	}
	if err = storageContext(ctx); err != nil {
		return "", err
	}
	return "", ErrStorageCapabilityUnavailable
}

// List scans the directory in bounded batches, retaining at most Limit+1 keys.
// It is weakly consistent during concurrent changes, not a filesystem snapshot.
func (l *Local) List(ctx context.Context, options ListOptions) (page Page, err error) {
	root, _, err := l.acquire()
	if err != nil {
		return Page{}, err
	}
	defer func() {
		finishRoot(root, &err)
		if err != nil {
			page = Page{}
		}
	}()
	if options.After != "" && !validKey(options.After) {
		return Page{}, ErrInvalidKey
	}
	if options.Limit == 0 {
		options.Limit = DefaultListLimit
	}
	if options.Limit < 1 || options.Limit > MaxListLimit {
		return Page{}, ErrLimit
	}
	if err = storageContext(ctx); err != nil {
		return Page{}, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return Page{}, ErrUnavailable
	}
	defer func() {
		if directory.Close() != nil {
			err = errors.Join(err, ErrUnavailable)
			page = Page{}
		}
	}()
	keys := make([]string, 0, options.Limit+1)
	for {
		if err = storageContext(ctx); err != nil {
			return Page{}, err
		}
		entries, readErr := directory.ReadDir(128)
		if readErr != nil && readErr != io.EOF {
			return Page{}, ErrUnavailable
		}
		for _, entry := range entries {
			key := entry.Name()
			if !validKey(key) || key <= options.After {
				continue
			}
			if _, infoErr := regularInfo(root, key); infoErr != nil {
				if infoErr == ErrNotFound {
					continue
				}
				return Page{}, infoErr
			}
			at := sort.SearchStrings(keys, key)
			if at < len(keys) && keys[at] == key {
				continue
			}
			if at >= options.Limit+1 {
				continue
			}
			if len(keys) < options.Limit+1 {
				keys = append(keys, "")
			}
			copy(keys[at+1:], keys[at:len(keys)-1])
			keys[at] = key
		}
		if readErr == io.EOF {
			break
		}
	}
	if err = storageContext(ctx); err != nil {
		return Page{}, err
	}
	if len(keys) > options.Limit {
		page.Next = keys[options.Limit-1]
		keys = keys[:options.Limit]
	}
	page.Keys = keys
	return page, nil
}
