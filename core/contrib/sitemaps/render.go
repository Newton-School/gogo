package sitemaps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
)

// Renderer owns immutable configuration. Providers and HTTP delivery are
// separate; rendering never queries a backend or starts background work.
type Renderer struct {
	urls                    *urlPolicy
	policy                  Policy
	limits                  Limits
	maxPages, maxBuildBytes int
}

func New(config Config) (renderer *Renderer, err error) {
	defer func() {
		if recover() != nil {
			renderer, err = nil, ErrUnavailable
		}
	}()
	if config.Policy.Entry == nil && config.Policy.Index == nil {
		return nil, ErrInvalid
	}
	if config.MaxItems == 0 {
		config.MaxItems = 1000
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = 1 << 20
	}
	if config.MaxPages == 0 {
		config.MaxPages = 100
	}
	if config.MaxBuildBytes == 0 {
		config.MaxBuildBytes = max(16<<20, config.MaxBytes)
	}
	if config.MaxItems < 1 || config.MaxItems > 50000 || config.MaxBytes < 1024 || config.MaxBytes > 50<<20 || config.MaxPages < 1 || config.MaxPages > 1000 || config.MaxBuildBytes < 1024 || config.MaxBuildBytes > 128<<20 {
		return nil, ErrLimit
	}
	urls, err := newURLPolicy(config)
	if err != nil {
		return nil, err
	}
	return &Renderer{urls: urls, policy: config.Policy, limits: Limits{MaxItems: config.MaxItems, MaxBytes: config.MaxBytes}, maxPages: config.MaxPages, maxBuildBytes: config.MaxBuildBytes}, nil
}

func (r *Renderer) operation(ctx context.Context) (Renderer, error) {
	if r == nil || r.urls == nil {
		return Renderer{}, ErrInvalid
	}
	// A Context implementation is application code too. Capture configuration
	// before consulting it, just as before publication callbacks.
	operation := *r
	if err := sitemapContextError(ctx); err != nil {
		return Renderer{}, err
	}
	return operation, nil
}

// RenderPage returns a complete authorized URL set, or no document. The input
// is normalized and detached before the first publication callback executes.
func (r *Renderer) RenderPage(ctx context.Context, entries []Entry) (document Document, err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if canceled := sitemapContextError(ctx); canceled != nil {
			err = canceled
		}
		if err != nil {
			document = Document{}
		}
	}()
	operation, err := r.operation(ctx)
	if err != nil {
		return Document{}, err
	}
	if operation.policy.Entry == nil {
		return Document{}, ErrInvalid
	}
	snapshot, err := operation.pageEntries(ctx, entries, operation.limits.MaxItems, operation.maxBuildBytes)
	if err != nil {
		return Document{}, err
	}
	if err := operation.authorizeEntries(ctx, snapshot); err != nil {
		return Document{}, err
	}
	body := &sitemapBuffer{limit: operation.limits.MaxBytes}
	if _, err := body.Write([]byte(sitemapPageOpen)); err != nil {
		return Document{}, err
	}
	for _, entry := range snapshot {
		if err := writeSitemapEntry(ctx, body, entry); err != nil {
			return Document{}, err
		}
	}
	if _, err := body.Write([]byte(sitemapPageClose)); err != nil {
		return Document{}, err
	}
	return sitemapDocument(body.Bytes()), nil
}

// RenderIndex requires publication authority for each referenced sitemap.
// Index LastMod values describe the referenced documents, not their item dates.
func (r *Renderer) RenderIndex(ctx context.Context, entries []IndexEntry) (document Document, err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if canceled := sitemapContextError(ctx); canceled != nil {
			err = canceled
		}
		if err != nil {
			document = Document{}
		}
	}()
	operation, err := r.operation(ctx)
	if err != nil {
		return Document{}, err
	}
	if operation.policy.Index == nil {
		return Document{}, ErrInvalid
	}
	if len(entries) > operation.limits.MaxItems {
		return Document{}, ErrLimit
	}
	remaining := operation.maxBuildBytes
	for _, entry := range entries {
		if err := sitemapContextError(ctx); err != nil {
			return Document{}, err
		}
		size, err := indexInputSize(entry)
		if err != nil {
			return Document{}, err
		}
		if size > remaining {
			return Document{}, ErrLimit
		}
		remaining -= size
	}
	snapshot := make([]IndexEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if err := sitemapContextError(ctx); err != nil {
			return Document{}, err
		}
		entry, err := operation.urls.normalizeIndex(entry)
		if err != nil {
			return Document{}, err
		}
		if seen[entry.Loc] {
			return Document{}, ErrInvalid
		}
		seen[entry.Loc] = true
		snapshot = append(snapshot, entry)
	}
	for _, entry := range snapshot {
		if err := sitemapContextError(ctx); err != nil {
			return Document{}, err
		}
		view := cloneIndexEntry(entry)
		if err := operation.policy.Index(ctx, view); err != nil {
			return Document{}, sitemapPolicyError(ctx, err)
		}
		if !reflect.DeepEqual(view, entry) {
			return Document{}, ErrInvalid
		}
	}
	body := &sitemapBuffer{limit: operation.limits.MaxBytes}
	if _, err := body.Write([]byte(sitemapIndexOpen)); err != nil {
		return Document{}, err
	}
	for _, entry := range snapshot {
		if err := writeSitemapIndexEntry(ctx, body, entry); err != nil {
			return Document{}, err
		}
	}
	if _, err := body.Write([]byte(sitemapIndexClose)); err != nil {
		return Document{}, err
	}
	return sitemapDocument(body.Bytes()), nil
}

func (r Renderer) pageEntries(ctx context.Context, entries []Entry, maxItems, budget int) ([]Entry, error) {
	if len(entries) > maxItems {
		return nil, ErrLimit
	}
	// Reject aggregate input before allocating the complete owned collection.
	// The normalizer separately bounds each entry and its alternate metadata.
	remaining := budget
	for _, entry := range entries {
		if err := sitemapContextError(ctx); err != nil {
			return nil, err
		}
		size, err := entryInputSize(entry)
		if err != nil {
			return nil, err
		}
		if size > remaining {
			return nil, ErrLimit
		}
		remaining -= size
	}
	snapshot := make([]Entry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if err := sitemapContextError(ctx); err != nil {
			return nil, err
		}
		entry, err := r.urls.normalizeEntry(entry)
		if err != nil {
			return nil, err
		}
		if seen[entry.Loc] {
			return nil, ErrInvalid
		}
		seen[entry.Loc] = true
		snapshot = append(snapshot, entry)
	}
	return snapshot, nil
}

func (r Renderer) authorizeEntries(ctx context.Context, entries []Entry) error {
	for _, entry := range entries {
		if err := sitemapContextError(ctx); err != nil {
			return err
		}
		if len(entry.Alternates) > 0 && r.policy.Alternate == nil {
			return ErrNotPublic
		}
		view := cloneEntry(entry)
		if err := r.policy.Entry(ctx, view); err != nil {
			return sitemapPolicyError(ctx, err)
		}
		if !reflect.DeepEqual(view, entry) {
			return ErrInvalid
		}
		for _, alternate := range entry.Alternates {
			if err := sitemapContextError(ctx); err != nil {
				return err
			}
			view := cloneEntry(entry)
			if err := r.policy.Alternate(ctx, view, alternate); err != nil {
				return sitemapPolicyError(ctx, err)
			}
			if !reflect.DeepEqual(view, entry) {
				return ErrInvalid
			}
		}
	}
	return sitemapContextError(ctx)
}

func sitemapPolicyError(ctx context.Context, err error) error {
	if canceled := sitemapContextError(ctx); canceled != nil {
		return canceled
	}
	if err == ErrNotPublic {
		return ErrNotPublic
	}
	return ErrUnavailable
}

func sitemapContextError(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	if ctx == nil {
		return ErrInvalid
	}
	value := reflect.ValueOf(ctx)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return ErrInvalid
		}
	}
	return ctx.Err()
}

// body is freshly allocated private output and is never modified after this
// ownership transfer. No input, another document or renderer borrows its bytes.
func sitemapDocument(body []byte) Document {
	sum := sha256.Sum256(body)
	return Document{Body: body, ContentType: "application/xml; charset=utf-8", ETag: `"` + hex.EncodeToString(sum[:]) + `"`}
}
