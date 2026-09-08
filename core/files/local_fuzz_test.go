package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fuzzStorageReader struct {
	reader *bytes.Reader
	cancel context.CancelFunc
	local  *Local
	mode   uint8
	calls  int
}

func (r *fuzzStorageReader) Read(p []byte) (int, error) {
	r.calls++
	if r.calls == 1 {
		switch r.mode {
		case 1:
			r.cancel()
		case 2:
			_ = r.local.Close()
		case 3:
			panic("private reader details")
		case 4:
			return len(p) + 1, nil
		}
	}
	return r.reader.Read(p)
}

// Exercise the filesystem effects, not only the key grammar: a rejected or
// canceled save may leave neither a published object nor an owned staging file.
func FuzzLocalStoragePublicationBoundary(f *testing.F) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		f.Skip("local storage requires a supported rooted filesystem")
	}
	const canary = "00000000000000000000000000000000"
	const target = "11111111111111111111111111111111"
	for _, key := range []string{target, canary, "", "../escape", target + "/", strings.ToUpper("abcdefabcdefabcdefabcdefabcdefab"), "x\x00y", "/absolute", `..\escape`} {
		f.Add(key, []byte("sample"), uint8(0))
	}
	for mode := uint8(1); mode <= 4; mode++ {
		f.Add(target, []byte("sample"), mode)
	}
	f.Add(target, []byte{}, uint8(0))
	f.Add(target, bytes.Repeat([]byte("x"), 1025), uint8(0))
	f.Fuzz(func(t *testing.T, key string, payload []byte, mode uint8) {
		if len(key) > 256 || len(payload) > 2048 {
			t.Skip()
		}
		mode %= 5
		root := t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, canary), []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
		local, err := NewLocal(LocalConfig{Directory: root, MaxBytes: 1024})
		if err != nil {
			t.Fatal(err)
		}
		defer local.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reader := &fuzzStorageReader{reader: bytes.NewReader(payload), cancel: cancel, local: local, mode: mode}
		result, saveErr := local.Save(ctx, key, reader)
		valid := len(key) == 32 && strings.IndexFunc(key, func(r rune) bool {
			return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
		}) == -1
		if valid != validKey(key) {
			t.Fatal("opaque key grammar changed")
		}
		var expected error
		switch {
		case !valid:
			expected = ErrInvalidKey
			if reader.calls != 0 {
				t.Fatal("invalid key consumed reader")
			}
		case mode == 1:
			expected = context.Canceled
		case mode == 3 || mode == 4:
			expected = ErrUnavailable
		case len(payload) > 1024:
			expected = ErrLimit
		case key == canary:
			expected = ErrCollision
		}
		if expected != nil {
			if !errors.Is(saveErr, expected) || result != (SaveResult{}) {
				t.Fatal("unpublished result or safe error changed", result, saveErr)
			}
		} else {
			digest := sha256.Sum256(payload)
			if saveErr != nil || !result.Published || result.Key != key || result.Bytes != int64(len(payload)) || result.SHA256 != hex.EncodeToString(digest[:]) || result.ModifiedTime.IsZero() {
				t.Fatal("publication outcome lost", result, saveErr)
			}
			actual, err := os.ReadFile(filepath.Join(root, key))
			if err != nil || !bytes.Equal(actual, payload) {
				t.Fatal("published bytes differ", err)
			}
			info, err := os.Stat(filepath.Join(root, key))
			if err != nil || info.Mode().Perm()&0077 != 0 {
				t.Fatal("published object is not private", err)
			}
			if mode != 2 {
				opened, err := local.Open(context.Background(), key)
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := io.ReadAll(opened)
				closeErr := opened.Close()
				if readErr != nil || closeErr != nil || !bytes.Equal(data, payload) {
					t.Fatal("stored bytes cannot be reopened", readErr, closeErr)
				}
			}
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() != canary && !(result.Published && entry.Name() == key) {
				t.Fatal("unpublished or temporary file remains", entry.Name())
			}
		}
		preserved, err := os.ReadFile(filepath.Join(root, canary))
		if err != nil || string(preserved) != "preserve" {
			t.Fatal("collision overwrote existing object", err)
		}
	})
}
