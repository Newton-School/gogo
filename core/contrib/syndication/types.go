// Package syndication builds bounded RSS and Atom documents from explicitly
// public, application-authorized feed metadata. It never fetches linked assets.
package syndication

import (
	"context"
	"errors"
	"io"
	"time"
)

var ErrInvalidFeed = errors.New("syndication: invalid feed metadata")
var ErrLimit = errors.New("syndication: feed exceeds configured limits")
var ErrUnavailable = errors.New("syndication: feed generation unavailable")
var ErrNotPublic = errors.New("syndication: content is not public")

// Content is plain text unless HTML is explicitly set by trusted application
// code after its content sanitization policy. XML encoding never uses raw XML.
type Content struct {
	Value string
	HTML  bool
}

type Author struct{ Name, Email, URL string }
type Category struct{ Term, Scheme, Label string }
type Enclosure struct {
	URL, MIMEType string
	Length        int64
}

type Item struct {
	ID, Title, Link     string
	IDIsPermalink       bool
	Description         Content
	Author              *Author
	Published, Updated  time.Time
	Categories          []Category
	Enclosures          []Enclosure
	Comments, Copyright string
}

type Feed struct {
	ID, Title, Link, FeedURL       string
	Description                    Content
	Language, Copyright, Generator string
	Author                         *Author
	Updated                        time.Time
	Categories                     []Category
	Items                          []Item
}

// Format implementations serialize normalized metadata. Encode must not close
// the writer or perform external reads/writes. Render owns validation, output
// bounds and the no-partial-document boundary for built-in and custom formats.
type Format interface {
	ContentType() string
	Encode(context.Context, io.Writer, Feed) error
}

// RSS2 serializes RSS 2.0. It supports at most one enclosure per item.
type RSS2 struct{}

// Atom1 serializes Atom 1.0 with typed text/HTML constructs and enclosure links.
type Atom1 struct{}

// Policy is the trusted application's public-content decision. Every item and
// enclosure is checked before XML is emitted; errors never omit items silently.
// Checks receive detached metadata and must be read-only. Site selection is not
// tenant authorization. The callbacks must not accept private signed URLs.
type Policy struct {
	Item      func(context.Context, Item) error
	Enclosure func(context.Context, Item, Enclosure) error
}

type Config struct {
	// Origin is a configured HTTP(S) origin, never an unvalidated request Host.
	Origin string
	// AdditionalOrigins explicitly permits external item/author/enclosure URLs.
	// Feed link and self URL remain bound to Origin.
	AdditionalOrigins  []string
	Format             Format
	Policy             Policy
	MaxItems, MaxBytes int
}

type Document struct {
	Body              []byte
	ContentType, ETag string
	LastModified      time.Time
}
