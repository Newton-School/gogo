package sitemaps

import "context"

// BuildPages partitions a bounded, stable input in order, keeping each entry
// intact. It accounts for actual escaped XML and every document frame. No page
// is returned unless the complete input and every publication check succeeds.
func (r *Renderer) BuildPages(ctx context.Context, entries []Entry) (documents []Document, err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if canceled := sitemapContextError(ctx); canceled != nil {
			err = canceled
		}
		if err != nil {
			documents = nil
		}
	}()
	operation, err := r.operation(ctx)
	if err != nil {
		return nil, err
	}
	if operation.policy.Entry == nil {
		return nil, ErrInvalid
	}
	maxItems := min(operation.limits.MaxItems*operation.maxPages, 1000000)
	snapshot, err := operation.pageEntries(ctx, entries, maxItems, operation.maxBuildBytes)
	if err != nil {
		return nil, err
	}
	if err := operation.authorizeEntries(ctx, snapshot); err != nil {
		return nil, err
	}
	frameSize := len(sitemapPageOpen) + len(sitemapPageClose)
	remaining := operation.maxBuildBytes
	var page *sitemapBuffer
	pageItems := 0
	newPage := func() error {
		if len(documents) >= operation.maxPages || frameSize > remaining {
			return ErrLimit
		}
		page = &sitemapBuffer{limit: operation.limits.MaxBytes}
		pageItems = 0
		_, err := page.Write([]byte(sitemapPageOpen))
		return err
	}
	finishPage := func() error {
		if _, err := page.Write([]byte(sitemapPageClose)); err != nil {
			return err
		}
		if page.Len() > remaining {
			return ErrLimit
		}
		remaining -= page.Len()
		documents = append(documents, sitemapDocument(page.Bytes()))
		return nil
	}
	if err := newPage(); err != nil {
		return nil, err
	}
	for _, entry := range snapshot {
		if err := sitemapContextError(ctx); err != nil {
			return nil, err
		}
		fragment := &sitemapBuffer{limit: operation.limits.MaxBytes - frameSize}
		if err := writeSitemapEntry(ctx, fragment, entry); err != nil {
			return nil, err
		}
		if pageItems == operation.limits.MaxItems || fragment.Len() > operation.limits.MaxBytes-page.Len()-len(sitemapPageClose) {
			if err := finishPage(); err != nil {
				return nil, err
			}
			if err := newPage(); err != nil {
				return nil, err
			}
		}
		// Reserve the closing frame before accumulating more private bytes.
		if fragment.Len() > remaining-page.Len()-len(sitemapPageClose) {
			return nil, ErrLimit
		}
		if _, err := page.Write(fragment.Bytes()); err != nil {
			return nil, err
		}
		pageItems++
	}
	if err := finishPage(); err != nil {
		return nil, err
	}
	return documents, nil
}
