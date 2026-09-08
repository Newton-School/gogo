// Package sitemaps builds bounded public sitemap pages and indexes. Application
// providers own publication scope, stable ordering and revision consistency.
package sitemaps

import (
	"context"
	"errors"
	"time"
)

var ErrInvalid = errors.New("sitemaps: invalid metadata or configuration")
var ErrLimit = errors.New("sitemaps: configured limit exceeded")
var ErrNotPublic = errors.New("sitemaps: content is not public")
var ErrUnavailable = errors.New("sitemaps: generation unavailable")
var ErrStale = errors.New("sitemaps: page revision is no longer available")

// LastModified is optional. Set Date (YYYY-MM-DD) OR Time, never both. A date
// remains a calendar date; no midnight or timezone conversion is invented.
type LastModified struct {
	Date string
	Time time.Time
}

type Alternate struct{ Language, Loc string }

type Entry struct {
	Loc        string
	LastMod    LastModified
	ChangeFreq string
	Priority   *float64
	Alternates []Alternate
}

type IndexEntry struct {
	Loc     string
	LastMod LastModified
}

// Policy receives detached, read-only metadata. A configured site/origin is not
// publication authority. Checks must be concurrency-safe and honor context.
type Policy struct {
	Entry     func(context.Context, Entry) error
	Alternate func(context.Context, Entry, Alternate) error
	Index     func(context.Context, IndexEntry) error
}

type Config struct {
	Origin string
	// Directory is the root-relative directory in which documents are served;
	// it defaults to /. Primary URLs must be below it on the configured origin.
	Directory string
	// AdditionalOrigins applies only to explicitly authorized locale alternates.
	AdditionalOrigins []string
	Policy            Policy
	// Defaults: 1000 entries, 1 MiB per document, 100 pages, 16 MiB per build.
	// Protocol ceilings: 50,000 entries and 50 MiB per document. Whole builds
	// are also bounded, even when each document individually fits its limit.
	// MaxBuildBytes bounds owned input metadata in every operation and total
	// encoded output in BuildPages. MaxBytes measures actual XML per document.
	MaxItems, MaxBytes, MaxPages, MaxBuildBytes int
}

type Limits struct{ MaxItems, MaxBytes int }

type Document struct {
	Body              []byte
	ContentType, ETag string
}

// PageRef is a provider-owned stable page identity. Key is a URL-safe opaque
// token; Revision binds a manifest entry to its exact published snapshot.
// LastMod describes the sitemap document, not max(item modification time).
type PageRef struct {
	Key, Revision string
	LastMod       LastModified
}

type Page struct {
	Ref     PageRef
	Entries []Entry
}

// Provider must not reinterpret section/page selectors as database names or
// broaden the configured scope. Manifest returns at most limit records in a
// stable order. LoadPage returns the exact requested ref or ErrStale; it must
// reject overflow instead of silently truncating and propagate provider errors.
type Provider interface {
	Manifest(context.Context, int) ([]PageRef, error)
	LoadPage(context.Context, PageRef, Limits) (Page, error)
}

type Section struct {
	Name     string
	Provider Provider
}

type HandlerOptions struct {
	// Authorize runs before reading the named section's manifest or page.
	// Index generation calls it for every registered section.
	Authorize   func(context.Context, string) error
	Timeout     time.Duration
	PublicCache bool
}
