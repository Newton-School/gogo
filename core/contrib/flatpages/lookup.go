package flatpages

import (
	"context"
	"database/sql"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/orm"
)

// Lookup is a server-side content read, not an authorization grant. A handler
// must authorize the reader before exposing content or invoking a sanitizer.
func (s *Store) Lookup(ctx context.Context, input LookupInput) (info Info, found bool, err error) {
	if s == nil || s.state == nil {
		return Info{}, false, ErrConfiguration
	}
	state := *s.state
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if canceled := contextError(ctx); canceled != nil {
			err = canceled
		}
		if err != nil {
			info, found = Info{}, false
		}
	}()
	if err := contextError(ctx); err != nil {
		return Info{}, false, err
	}
	site, err := pageID(input.SiteID)
	if err != nil {
		return Info{}, false, ErrSiteNotConfigured
	}
	key, err := normalizePath(input.URL)
	if err != nil {
		return Info{}, false, ErrInvalid
	}
	if db.InTransaction(ctx, state.alias) {
		return Info{}, false, ErrTransaction
	}
	backend, store := state.operation()
	var outcome error
	err = db.Atomic(ctx, backend, db.AtomicOptions{TxOptions: db.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, Durable: true}, func(txctx context.Context) error {
		current, exists, e := readSite(txctx, store, backend.tx, site, false)
		if e != nil {
			outcome = e
			return e
		}
		if !exists || !current.Active {
			outcome = ErrSiteNotConfigured
			return outcome
		}
		links, e := readLinks(txctx, store, backend.tx, db.Predicate{Connector: "AND", Children: []db.Predicate{orm.Q("site_id", site), orm.Q("url", key)}}, 1, false)
		if e != nil {
			outcome = ErrUnavailable
			return outcome
		}
		if len(links) == 0 {
			return nil
		}
		link := links[0]
		if link.SiteID != site || link.URL != key {
			outcome = ErrUnavailable
			return outcome
		}
		info, found, e = readPage(txctx, store, backend.tx, link.PageID, false)
		if e != nil {
			outcome = e
			return e
		}
		if !found || info.URL != key {
			outcome = ErrUnavailable
			return outcome
		}
		outcome = contextError(txctx)
		return outcome
	})
	if err != nil && err != outcome {
		err = ErrUnavailable
	}
	return info, found, err
}

type linkInfo struct{ ID, PageID, SiteID, URL string }
type siteInfo struct {
	ID, Domain, DisplayName string
	Active                  bool
}

func readPage(ctx context.Context, store *orm.Store, executor db.Executor, id string, lock bool) (Info, bool, error) {
	query := orm.For(store, func() *FlatPage { return &FlatPage{} }).Filter(orm.Q("id", id)).Limit(2)
	if lock {
		query = query.SelectForUpdate(false, false)
	}
	statement, args, err := query.SQLContext(ctx)
	if err != nil {
		return Info{}, false, ErrUnavailable
	}
	var info Info
	count, err := readRows(ctx, executor, statement, args, 1, func(rows db.Rows) error {
		return rows.Scan(&info.ID, &info.URL, &info.Title, &info.Content, &info.TemplateName, &info.RegistrationRequired)
	})
	if err != nil {
		return Info{}, false, err
	}
	if count == 0 {
		return Info{}, false, nil
	}
	canonical, err := pageID(info.ID)
	if err != nil || canonical != id || info.ID != id {
		return Info{}, false, ErrUnavailable
	}
	draft, err := validateDraft(infoDraft(info))
	if err != nil || draftInfo(draft) != info {
		return Info{}, false, ErrUnavailable
	}
	return info, true, nil
}
func readSite(ctx context.Context, store *orm.Store, executor db.Executor, id string, lock bool) (siteInfo, bool, error) {
	query := orm.For(store, func() *sites.Site { return &sites.Site{} }).Filter(orm.Q("id", id)).Limit(2)
	if lock {
		query = query.SelectForUpdate(false, false)
	}
	statement, args, err := query.SQLContext(ctx)
	if err != nil {
		return siteInfo{}, false, ErrUnavailable
	}
	var info siteInfo
	count, err := readRows(ctx, executor, statement, args, 1, func(rows db.Rows) error { return rows.Scan(&info.ID, &info.Domain, &info.DisplayName, &info.Active) })
	if err != nil {
		return siteInfo{}, false, err
	}
	if count == 0 {
		return siteInfo{}, false, nil
	}
	model := &sites.Site{ID: info.ID, Domain: info.Domain, DisplayName: info.DisplayName, Active: info.Active}
	if err := model.Clean(context.Background()); err != nil || info.ID != id || model.ID != id || model.Domain != info.Domain {
		return siteInfo{}, false, ErrUnavailable
	}
	return info, true, nil
}
func readLinks(ctx context.Context, store *orm.Store, executor db.Executor, where db.Predicate, maximum int, lock bool) (links []linkInfo, err error) {
	query := orm.For(store, func() *FlatPageSite { return &FlatPageSite{} }).Filter(where).Limit(maximum + 1)
	if lock {
		query = query.SelectForUpdate(false, false)
	}
	statement, args, err := query.SQLContext(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	_, err = readRows(ctx, executor, statement, args, maximum, func(rows db.Rows) error {
		var info linkInfo
		if err := rows.Scan(&info.ID, &info.PageID, &info.SiteID, &info.URL); err != nil {
			return err
		}
		for _, id := range []string{info.ID, info.PageID, info.SiteID} {
			if canonical, e := pageID(id); e != nil || canonical != id {
				return ErrUnavailable
			}
		}
		if path, e := normalizePath(info.URL); e != nil || path != info.URL {
			return ErrUnavailable
		}
		links = append(links, info)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return links, nil
}

// readRows checks bounded cardinality, Scan, terminal Next/Err and Close before
// exposing data. Providers are trusted storage boundaries, not HTTP inputs.
func readRows(ctx context.Context, executor db.Executor, statement string, args []any, maximum int, scan func(db.Rows) error) (count int, err error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	rows, queryErr := executor.Query(ctx, statement, args...)
	if !nilValue(rows) {
		defer func() {
			closeErr := rows.Close()
			if closeErr != nil || rows.Err() != nil {
				count, err = 0, ErrUnavailable
			}
			if canceled := contextError(ctx); canceled != nil {
				count, err = 0, canceled
			}
		}()
	}
	if queryErr != nil || nilValue(rows) {
		return 0, ErrUnavailable
	}
	for rows.Next() {
		if count == maximum {
			return 0, ErrLimit
		}
		if err := scan(rows); err != nil {
			return 0, ErrUnavailable
		}
		count++
	}
	if rows.Err() != nil {
		return 0, ErrUnavailable
	}
	return count, nil
}
