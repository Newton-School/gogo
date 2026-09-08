package static

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/core/app"
)

// Register contributes public source declarations during an app's Register
// hook. It performs no source I/O. Labels must match the installed app label.
func Register(registry *app.Registry, label string, sources ...Source) error {
	if registry == nil || !validOwner(label) || len(sources) == 0 || len(sources) > 128 {
		return ErrInvalid
	}
	owned := append([]Source(nil), sources...)
	for i := range owned {
		if owned[i].Owner == "" {
			owned[i].Owner = label
		}
	}
	if _, err := New(Config{Sources: owned, BaseURL: "/static/"}); err != nil {
		return err
	}
	return registry.Register("static", label, owned)
}

// AppSources resolves only explicitly installed apps, in the dependency order
// used by app.Prepare. Project sources are prepended by the caller. Registry
// Names is intentionally not used as asset precedence.
func AppSources(registry *app.Registry, configs []app.Config) ([]Source, error) {
	if registry == nil || len(configs) > 128 {
		return nil, ErrInvalid
	}
	ordered, err := app.TopologicalOrder(configs)
	if err != nil {
		return nil, fail(ErrInvalid, err)
	}
	var result []Source
	for _, config := range ordered {
		value, ok := registry.Get("static", config.Label)
		if !ok {
			continue
		}
		sources, ok := value.([]Source)
		if !ok || len(sources) > 128-len(result) {
			return nil, ErrInvalid
		}
		result = append(result, sources...)
	}
	if len(result) > 0 {
		if _, err := New(Config{Sources: result, BaseURL: "/static/"}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

type sourceRoot struct {
	fs    fs.FS
	local *os.Root
}

func openSource(s Source) (sourceRoot, error) {
	if s.Directory == "" {
		return sourceRoot{fs: s.FS}, nil
	}
	info, err := os.Lstat(s.Directory)
	if err != nil {
		return sourceRoot{}, fail(ErrSource, err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return sourceRoot{}, ErrInvalid
	}
	root, err := os.OpenRoot(s.Directory)
	if err != nil {
		return sourceRoot{}, fail(ErrSource, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return sourceRoot{}, fail(ErrSource, errors.Join(err, root.Close()))
	}
	return sourceRoot{fs: root.FS(), local: root}, nil
}
func (s sourceRoot) close() error {
	if s.local != nil {
		return s.local.Close()
	}
	return nil
}

type discovered struct {
	matches []Match
	assets  map[string][]byte
}

func (c Collector) discover(ctx context.Context, read bool) (result discovered, err error) {
	defer func() {
		if recover() != nil {
			result = discovered{}
			err = fail(ErrSource, contextError(ctx))
		}
	}()
	result.assets = map[string][]byte{}
	seen := map[string]bool{}
	entries := 0
	var total int64
	for index, source := range c.config.Sources {
		if err = contextError(ctx); err != nil {
			return discovered{}, err
		}
		root, e := openSource(source)
		if e != nil {
			return discovered{}, e
		}
		err = func() (err error) {
			defer func() { err = errors.Join(err, root.close()) }()
			var walk func(string, int) error
			walk = func(directory string, depth int) error {
				if depth > 64 {
					return ErrLimit
				}
				if e := contextError(ctx); e != nil {
					return e
				}
				file, e := root.fs.Open(directory)
				if e != nil {
					return fail(ErrSource, e)
				}
				list, e := readDirectory(ctx, file, c.config.Limits.MaxEntries-entries)
				if e != nil {
					return e
				}
				entries += len(list)
				sort.Slice(list, func(i, j int) bool { return list[i].Name() < list[j].Name() })
				for _, entry := range list {
					name := entry.Name()
					if strings.Contains(name, "/") || !validPath(name) {
						return ErrInvalid
					}
					logical := name
					if directory != "." {
						logical = path.Join(directory, name)
					}
					if !validPath(logical) {
						return ErrInvalid
					}
					if entry.Type()&fs.ModeSymlink != 0 {
						return ErrInvalid
					}
					if ignoredPath(logical) {
						continue
					}
					if root.local != nil {
						info, e := root.local.Lstat(logical)
						if e != nil {
							return fail(ErrSource, e)
						}
						if info.Mode()&fs.ModeSymlink != 0 {
							return ErrInvalid
						}
					}
					if entry.IsDir() {
						if e := walk(logical, depth+1); e != nil {
							return e
						}
						continue
					}
					if !entry.Type().IsRegular() {
						return ErrInvalid
					}
					chosen := !seen[logical]
					seen[logical] = true
					result.matches = append(result.matches, Match{Path: logical, Owner: source.Owner, Source: index, Selected: chosen})
					if read {
						data, e := readAsset(ctx, root.fs, logical, min(c.config.Limits.MaxFileBytes, c.config.Limits.MaxTotalBytes-total))
						if e != nil {
							return e
						}
						if int64(len(data)) > c.config.Limits.MaxTotalBytes-total {
							return ErrLimit
						}
						total += int64(len(data))
						if chosen {
							result.assets[logical] = data
						}
					}
				}
				return nil
			}
			return walk(".", 0)
		}()
		if err != nil {
			return discovered{}, fail(ErrSource, err)
		}
	}
	if err = contextError(ctx); err != nil {
		return discovered{}, err
	}
	return result, nil
}

func readDirectory(ctx context.Context, file fs.File, remaining int) (result []fs.DirEntry, err error) {
	defer func() { err = errors.Join(err, file.Close()) }()
	dir, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, ErrSource
	}
	for {
		if err = contextError(ctx); err != nil {
			return nil, err
		}
		batch, e := dir.ReadDir(min(128, remaining-len(result)+1))
		if len(batch) > remaining-len(result) {
			return nil, ErrLimit
		}
		result = append(result, batch...)
		if e == io.EOF {
			return result, nil
		}
		if e != nil {
			return nil, fail(ErrSource, e)
		}
		if len(batch) == 0 {
			return nil, ErrSource
		}
	}
}
func readAsset(ctx context.Context, filesystem fs.FS, name string, maxBytes int64) (data []byte, err error) {
	file, e := filesystem.Open(name)
	if e != nil {
		return nil, fail(ErrSource, e)
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			data = nil
		}
	}()
	info, e := file.Stat()
	if e != nil {
		return nil, fail(ErrSource, e)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return nil, ErrLimit
	}
	data, e = io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, maxBytes+1))
	if e != nil {
		return nil, fail(ErrSource, e)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrLimit
	}
	if e = contextError(ctx); e != nil {
		return nil, e
	}
	return data, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if e := contextError(r.ctx); e != nil {
		return 0, e
	}
	return r.reader.Read(p)
}

// Find returns every origin in precedence order. It reads directory metadata,
// not file contents, and never opens the collection destination.
func (c *Collector) Find(ctx context.Context, name string) ([]Match, error) {
	owned, err := c.operation(ctx)
	if err != nil {
		return nil, err
	}
	if !validPath(name) || ignoredPath(name) {
		return nil, ErrInvalid
	}
	result, err := owned.discover(ctx, false)
	if err != nil {
		return nil, err
	}
	var matches []Match
	for _, match := range result.matches {
		if match.Path == name {
			matches = append(matches, match)
		}
	}
	if len(matches) == 0 {
		return nil, ErrNotFound
	}
	return matches, nil
}
