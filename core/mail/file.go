package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
)

// File is a development-only, bounded MIME outbox. The supplied directory must
// already exist with private permissions. It never overwrites or deletes old
// messages. Quotas include existing entries; do not share this directory with
// other writers because filesystem quotas are not a multiprocess coordinator.
// Local filesystem calls may outlive cancellation; no abandoned goroutine is
// started to pretend that an uninterruptible filesystem operation was canceled.
type File struct{ *fileState }
type fileState struct {
	mu                    sync.Mutex
	root                  *os.Root
	limits                Limits
	maximum, maximumBytes int
	closed                bool
}
type FileConfig struct {
	Directory             string
	Limits                Limits
	MaxMessages, MaxBytes int
}

func (FileConfig) String() string                      { return "mail.FileConfig{directory:redacted}" }
func (c FileConfig) GoString() string                  { return c.String() }
func (c FileConfig) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, c.String()) }

func NewFile(config FileConfig) (*File, error) {
	if config.Directory == "" {
		return nil, ErrValidation
	}
	if config.MaxMessages == 0 {
		config.MaxMessages = 1000
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = 100 << 20
	}
	if config.MaxMessages < 1 || config.MaxMessages > 10000 || config.MaxBytes < 1 {
		return nil, ErrLimit
	}
	limits, err := config.Limits.defaults()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(config.Directory)
	if err != nil {
		return nil, &SendError{Kind: ErrTransport, Stage: "file_open"}
	}
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		_ = root.Close()
		return nil, ErrValidation
	}
	backend := &File{&fileState{root: root, limits: limits, maximum: config.MaxMessages, maximumBytes: config.MaxBytes}}
	if err := backend.capacity(0); err != nil {
		_ = root.Close()
		return nil, err
	}
	return backend, nil
}

func (File) String() string                      { return "mail.File{outbox:redacted}" }
func (f File) GoString() string                  { return f.String() }
func (f File) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, f.String()) }
func (File) MarshalJSON() ([]byte, error)        { return nil, ErrSensitiveOutput }

func (f *File) capacity(size int) error {
	directory, err := f.root.Open(".")
	if err != nil {
		return &SendError{Kind: ErrTransport, Stage: "file_list"}
	}
	defer directory.Close()
	entries, err := directory.ReadDir(f.maximum + 1)
	if err != nil && err != io.EOF {
		return &SendError{Kind: ErrTransport, Stage: "file_list"}
	}
	if len(entries) >= f.maximum {
		return ErrLimit
	}
	remaining := f.maximumBytes
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return &SendError{Kind: ErrTransport, Stage: "file_list"}
		}
		if !info.Mode().IsRegular() {
			return ErrValidation
		}
		if info.Size() > int64(remaining) {
			return ErrLimit
		}
		remaining -= int(info.Size())
	}
	if size > remaining {
		return ErrLimit
	}
	return nil
}

func (f *File) Send(ctx context.Context, message Message) (Receipt, error) {
	if message.Sensitive {
		return Receipt{}, ErrSensitiveOutput
	}
	p, err := Prepare(ctx, message, f.limits)
	if err != nil {
		return Receipt{}, err
	}
	r := receiptFor("file", p)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return r, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if err := f.capacity(len(p.data)); err != nil {
		return r, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return r, &SendError{Kind: ErrTransport, Stage: "file_identity"}
	}
	name := hex.EncodeToString(random[:])
	temporary := name + ".tmp"
	destination := name + ".eml"
	out, err := f.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return r, &SendError{Kind: ErrTransport, Stage: "file_open"}
	}
	defer func() { _ = out.Close(); _ = f.root.Remove(temporary) }()
	for offset := 0; offset < len(p.data); {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		end := min(len(p.data), offset+64*1024)
		n, err := out.Write(p.data[offset:end])
		if err != nil || n != end-offset {
			return r, &SendError{Kind: ErrTransport, Stage: "file_write"}
		}
		offset = end
	}
	if out.Sync() != nil || out.Close() != nil {
		return r, &SendError{Kind: ErrTransport, Stage: "file_sync"}
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	// Link publishes a complete file without replacement of an existing name.
	if f.root.Link(temporary, destination) != nil {
		return r, &SendError{Kind: ErrTransport, Stage: "file_publish"}
	}
	return simulation("file", p), nil
}
func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.root.Close() != nil {
		return &SendError{Kind: ErrTransport, Stage: "file_close"}
	}
	return nil
}
