// Package static collects explicitly public assets into immutable hashed files.
// It is separate from private uploaded-file storage and opens no network service.
package static

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
)

var (
	ErrInvalid     = errors.New("static: invalid configuration or path")
	ErrNotFound    = errors.New("static: asset not found")
	ErrLimit       = errors.New("static: asset limit exceeded")
	ErrSource      = errors.New("static: source failed")
	ErrDependency  = errors.New("static: invalid CSS dependency")
	ErrPublish     = errors.New("static: publication failed")
	ErrNotDebug    = errors.New("static: development serving requires DEBUG")
	ErrUnsupported = errors.New("static: local publication is unsupported on this platform")
)

// Source declares a public asset root. Exactly one of Directory or FS is set.
// Sources are searched in declaration order; the first occurrence wins.
// Directory uses rooted operating-system descriptors. FS is an explicitly
// trusted, cooperative provider (for example embed.FS), not a filesystem sandbox.
// Its files must remain regular and support bounded directory iteration.
type Source struct {
	Owner     string
	Directory string
	FS        fs.FS
}

// Limits are per operation. Zero selects the documented default, not unlimited.
// Counts include shadowed files and directory entries, preventing duplicates or
// ignored paths from bypassing discovery's bounds.
type Limits struct {
	MaxEntries    int
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxCSSBytes   int64
	MaxReferences int
}

type Config struct {
	Sources []Source
	// Destination is required for Collect, including dry-run. It must be a
	// separate local directory, never a source root or an ancestor/descendant.
	// Find and development serving do not require a destination.
	Destination string
	// BaseURL is an explicit local absolute prefix or HTTP(S) CDN prefix.
	// It ends in a slash and contains no query, fragment or credentials.
	BaseURL string
	Limits  Limits
}

// Collector is an immutable declaration handle. Config slices are captured;
// opaque FS providers retain their own concurrency/cooperation contract.
type Collector struct {
	config Config
	prefix urlPrefix
}

type CollectOptions struct{ DryRun bool }

// Match explains precedence without exposing an absolute filesystem path.
type Match struct {
	Path     string `json:"path"`
	Owner    string `json:"owner"`
	Source   int    `json:"source"`
	Selected bool   `json:"selected"`
}

// Report contains a fully prepared result. Published is true only after the
// manifest rename was observed to succeed. An error with Published=true means
// publication occurred (for example a subsequent directory sync failed); it
// must not be described as a rollback or automatically retried.
type Report struct {
	DryRun    bool
	Published bool
	Manifest  *Manifest
	Matches   []Match
}

type staticFailure struct{ kind, cause error }

func (e *staticFailure) Error() string              { return e.kind.Error() }
func (e *staticFailure) Unwrap() []error            { return []error{e.kind, e.cause} }
func (e *staticFailure) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, e.Error()) }
func fail(kind, cause error) error {
	if cause == nil {
		return kind
	}
	return &staticFailure{kind, cause}
}

func nilValue(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}
func contextError(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrInvalid
		}
	}()
	if nilValue(ctx) {
		return ErrInvalid
	}
	switch err := ctx.Err(); err {
	case nil, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrSource
	}
}

func normalizeLimits(l Limits) (Limits, error) {
	if l.MaxEntries == 0 {
		l.MaxEntries = 10000
	}
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = 16 << 20
	}
	if l.MaxTotalBytes == 0 {
		l.MaxTotalBytes = 256 << 20
	}
	if l.MaxCSSBytes == 0 {
		l.MaxCSSBytes = 2 << 20
	}
	if l.MaxReferences == 0 {
		l.MaxReferences = 100000
	}
	if l.MaxEntries < 1 || l.MaxEntries > 100000 || l.MaxFileBytes < 1 || l.MaxFileBytes > 256<<20 || l.MaxTotalBytes < 1 || l.MaxTotalBytes > 1<<30 || l.MaxCSSBytes < 1 || l.MaxCSSBytes > 16<<20 || l.MaxReferences < 1 || l.MaxReferences > 1000000 {
		return Limits{}, ErrInvalid
	}
	return l, nil
}

// New validates declarations without opening sources, files or services.
func New(config Config) (*Collector, error) {
	if len(config.Sources) == 0 || len(config.Sources) > 128 {
		return nil, ErrInvalid
	}
	config.Sources = append([]Source(nil), config.Sources...)
	for i, s := range config.Sources {
		if !validOwner(s.Owner) || (s.Directory == "") == nilValue(s.FS) || s.Directory != "" && !validDirectory(s.Directory) {
			return nil, ErrInvalid
		}
		if s.Directory != "" {
			absolute, err := filepath.Abs(s.Directory)
			if err != nil {
				return nil, ErrInvalid
			}
			config.Sources[i].Directory = absolute
		}
	}
	if config.Destination != "" && !validDirectory(config.Destination) {
		return nil, ErrInvalid
	}
	if config.Destination != "" {
		absolute, err := filepath.Abs(config.Destination)
		if err != nil || filepath.Dir(absolute) == absolute {
			return nil, ErrInvalid
		}
		config.Destination = absolute
	}
	var err error
	config.Limits, err = normalizeLimits(config.Limits)
	if err != nil {
		return nil, err
	}
	prefix, err := parsePrefix(config.BaseURL)
	if err != nil {
		return nil, err
	}
	config.BaseURL = prefix.base
	return &Collector{config: config, prefix: prefix}, nil
}

func (c *Collector) operation(ctx context.Context) (Collector, error) {
	if c == nil {
		return Collector{}, ErrInvalid
	}
	owned := *c
	if len(owned.config.Sources) == 0 {
		return Collector{}, ErrInvalid
	}
	if err := contextError(ctx); err != nil {
		return Collector{}, err
	}
	return owned, nil
}
