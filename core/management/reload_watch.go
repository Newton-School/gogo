package management

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	reloadMaxEntries   = 32768
	reloadMaxFileBytes = 16 << 20
	reloadMaxTreeBytes = 128 << 20
	reloadMaxDepth     = 32
)

type reloadWatcher struct {
	root            *os.Root
	identity        os.FileInfo
	directory       string
	extra, excluded []string
}

func reloadRelative(s string) bool {
	return len(s) > 0 && len(s) <= 1024 && utf8.ValidString(s) && s != "." && s != ".." && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\\\x00\r\n") && !path.IsAbs(s) && path.Clean(s) == s
}

func newReloadWatcher(directory string, extra []string, excluded []string) (*reloadWatcher, error) {
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	w := &reloadWatcher{root: root, identity: info, directory: directory, extra: slices.Clone(extra)}
	for _, name := range excluded {
		if name == "" {
			continue
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(canonical, name)
		}
		name, e := reloadResolvedPath(name)
		if e != nil {
			_ = root.Close()
			return nil, e
		}
		contains, e := filepath.Rel(name, canonical)
		if e != nil {
			_ = root.Close()
			return nil, e
		}
		if contains == "." || (contains != ".." && !strings.HasPrefix(contains, ".."+string(filepath.Separator))) {
			_ = root.Close()
			return nil, errors.New("output directory contains project sources")
		}
		rel, e := filepath.Rel(canonical, name)
		if e != nil {
			_ = root.Close()
			return nil, e
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			_ = root.Close()
			return nil, errors.New("output directory contains project sources")
		}
		if reloadRelative(rel) {
			w.excluded = append(w.excluded, rel)
		}
	}
	for _, name := range extra {
		if !reloadRelative(name) || w.ignored(name, true) {
			_ = root.Close()
			return nil, errors.New("invalid watch directory")
		}
		if err = w.regularDirectory(name); err != nil {
			_ = root.Close()
			return nil, err
		}
	}
	slices.Sort(w.excluded)
	w.excluded = slices.Compact(w.excluded)
	return w, nil
}

func (w *reloadWatcher) regularDirectory(name string) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := w.root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("watch directory must be a real directory")
		}
	}
	return nil
}

func (w *reloadWatcher) sameDirectory() error {
	info, err := os.Stat(w.directory)
	if err != nil {
		return err
	}
	if !os.SameFile(w.identity, info) {
		return errors.New("project directory identity changed")
	}
	return nil
}

func (w *reloadWatcher) ignored(name string, directory bool) bool {
	for _, excluded := range w.excluded {
		if name == excluded || strings.HasPrefix(name, excluded+"/") {
			return true
		}
	}
	for index, part := range strings.Split(name, "/") {
		if name == ".env" && !directory {
			continue
		}
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "#") || strings.HasSuffix(part, "~") || strings.HasSuffix(part, ".swp") || strings.HasSuffix(part, ".swo") || strings.HasSuffix(part, ".tmp") {
			return true
		}
		switch part {
		case "node_modules", "vendor", "__pycache__":
			return true
		}
		if index == 0 {
			switch part {
			case "bin", "build", "dist", "coverage", "uploads", "media":
				return true
			}
		}
	}
	return false
}

// Resolve existing ancestors too, so an absent output leaf below a symlink
// cannot disguise an output directory that contains the project itself.
func reloadResolvedPath(name string) (string, error) {
	name = filepath.Clean(name)
	var suffix []string
	for range 128 {
		resolved, err := filepath.EvalSymlinks(name)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(name)
		if parent == name {
			return "", err
		}
		suffix = append(suffix, filepath.Base(name))
		name = parent
	}
	return "", errors.New("output path exceeds depth limit")
}

func (w *reloadWatcher) selected(name string) bool {
	for _, extra := range w.extra {
		if strings.HasPrefix(name, extra+"/") {
			return true
		}
	}
	if strings.HasSuffix(name, ".go") {
		return true
	}
	if name == ".env" {
		return true
	}
	switch path.Base(name) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if part == "templates" {
			return true
		}
	}
	return false
}

// snapshot is deterministic and content-based, not mtime-based. It never
// follows discovered symlinks; transient reads fail closed and preserve the old
// child. Local authors are trusted to avoid adversarial in-place compiler races.
func (w *reloadWatcher) snapshot(ctx context.Context) (sum [32]byte, err error) {
	h := sha256.New()
	entriesLeft, bytesLeft := reloadMaxEntries, int64(reloadMaxTreeBytes)
	buffer := make([]byte, 32<<10)
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > reloadMaxDepth {
			return errors.New("source tree exceeds depth limit")
		}
		if e := reloadContextError(ctx); e != nil {
			return e
		}
		f, e := w.root.Open(dir)
		if e != nil {
			return e
		}
		var entries []os.DirEntry
		for {
			batch, readErr := f.ReadDir(128)
			entriesLeft -= len(batch)
			if entriesLeft < 0 {
				_ = f.Close()
				return errors.New("source tree exceeds entry limit")
			}
			entries = append(entries, batch...)
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = f.Close()
				return readErr
			}
		}
		if e = f.Close(); e != nil {
			return e
		}
		slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, entry := range entries {
			name := entry.Name()
			if dir != "." {
				name = dir + "/" + name
			}
			if !reloadRelative(name) {
				return errors.New("invalid source path")
			}
			if w.ignored(name, entry.IsDir()) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				if e = walk(name, depth+1); e != nil {
					return e
				}
				continue
			}
			if !w.selected(name) {
				continue
			}
			if e = reloadContextError(ctx); e != nil {
				return e
			}
			info, e := w.root.Lstat(name)
			if e != nil {
				return e
			}
			if !info.Mode().IsRegular() || info.Size() > reloadMaxFileBytes {
				return errors.New("unsupported source file")
			}
			file, e := w.root.Open(name)
			if e != nil {
				return e
			}
			opened, e := file.Stat()
			if e != nil || !os.SameFile(info, opened) {
				_ = file.Close()
				return errors.New("source file changed during open")
			}
			var length [8]byte
			binary.BigEndian.PutUint64(length[:], uint64(len(name)))
			_, _ = h.Write(length[:])
			_, _ = io.WriteString(h, name)
			fileHash := sha256.New()
			remaining := int64(reloadMaxFileBytes)
			for {
				if e = reloadContextError(ctx); e != nil {
					_ = file.Close()
					return e
				}
				n, readErr := file.Read(buffer)
				remaining -= int64(n)
				bytesLeft -= int64(n)
				if remaining < 0 || bytesLeft < 0 {
					_ = file.Close()
					return errors.New("source content exceeds limit")
				}
				_, _ = fileHash.Write(buffer[:n])
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					_ = file.Close()
					return readErr
				}
			}
			if e = file.Close(); e != nil {
				return e
			}
			_, _ = h.Write(fileHash.Sum(nil))
		}
		return nil
	}
	if err = w.sameDirectory(); err == nil {
		err = walk(".", 0)
	}
	if err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
