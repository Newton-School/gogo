package redirects

import (
	"context"
	"database/sql"

	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/orm"
)

// Resolver owns immutable configuration. Backend implementations remain trusted
// shared services; their internal state is not copied or synchronized here.
type Resolver struct{ state *resolverState }

type resolverState struct {
	backend                    db.Backend
	alias                      string
	appendSlash, preserveQuery bool
	maxHops                    int
}

// New validates configuration without opening a connection or migrating tables.
func New(config Config) (resolver *Resolver, err error) {
	defer func() {
		if recover() != nil {
			resolver, err = nil, ErrConfiguration
		}
	}()
	if nilValue(config.Backend) || config.MaxHops < 0 || config.MaxHops > MaxHops {
		return nil, ErrConfiguration
	}
	if config.MaxHops == 0 {
		config.MaxHops = DefaultMaxHops
	}
	alias := config.Backend.Alias()
	if alias == "" {
		return nil, ErrConfiguration
	}
	return &Resolver{state: &resolverState{backend: config.Backend, alias: alias, appendSlash: config.AppendSlash, preserveQuery: config.PreserveQuery, maxHops: config.MaxHops}}, nil
}

// Lookup checks the first redirect and its bounded local chain in an owned,
// durable, read-only repeatable-read transaction. Same-alias ambient atomic
// blocks are refused because savepoints cannot establish this snapshot mode.
// The returned match remains the first hop; external servers are never queried.
func (r *Resolver) Lookup(ctx context.Context, input LookupInput) (match Match, found bool, err error) {
	// Freeze value configuration before even Context.Err/Value or Backend calls.
	if r == nil || r.state == nil {
		return Match{}, false, ErrConfiguration
	}
	state := *r.state
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if canceled := contextError(ctx); canceled != nil {
			err = canceled
		}
		if err != nil {
			match, found = Match{}, false
		}
	}()
	if err = contextError(ctx); err != nil {
		return Match{}, false, err
	}
	siteID, e := redirectID(input.SiteID)
	if e != nil {
		return Match{}, false, ErrSiteNotConfigured
	}
	key, e := parseOldPath(input.URI)
	if e != nil {
		return Match{}, false, ErrInvalid
	}
	origin, e := normalizeOrigin(input.Origin)
	if e != nil {
		return Match{}, false, ErrInvalid
	}
	if db.InTransaction(ctx, state.alias) {
		return Match{}, false, ErrTransaction
	}
	backend := &lookupBackend{Backend: state.backend, alias: state.alias}
	var lookupErr error
	err = db.Atomic(ctx, backend, db.AtomicOptions{TxOptions: db.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, Durable: true}, func(txctx context.Context) error {
		store := orm.New(backend, nil)
		lookupErr = activeSite(txctx, store, backend.tx, siteID)
		if lookupErr == nil {
			match, found, lookupErr = state.walk(txctx, store, backend.tx, siteID, key, origin)
		}
		if canceled := contextError(txctx); canceled != nil {
			lookupErr = canceled
		}
		return lookupErr
	})
	// Cleanup/commit/provider failures never inherit a possibly partial domain
	// outcome. Only the exact closure error is an intentional lookup result.
	if err != nil && err != lookupErr {
		err = ErrUnavailable
	}
	return match, found, err
}

// Freeze alias routing and keep the exact returned transaction. A provider that
// returns a nil transaction is rejected before Atomic installs or uses it.
type lookupBackend struct {
	db.Backend
	alias string
	tx    db.Transaction
}

func (b *lookupBackend) Alias() string { return b.alias }
func (b *lookupBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		if !nilValue(tx) {
			// A partial provider resource is not installed in Atomic, so its
			// cleanup belongs here even when the original error is nonnil.
			_ = tx.Rollback()
		}
		return nil, ErrUnavailable
	}
	if nilValue(tx) {
		return nil, ErrUnavailable
	}
	b.tx = tx
	return tx, nil
}

func (s resolverState) walk(ctx context.Context, store *orm.Store, executor db.Executor, siteID, key, origin string) (Match, bool, error) {
	var first Match
	visited := make(map[string]bool, s.maxHops+1)
	rowsSeen := make(map[string]bool, s.maxHops)
	for hops := 0; ; hops++ {
		identity, err := cycleKey(key, origin)
		if err != nil {
			return Match{}, false, ErrInvalid
		}
		if visited[identity] {
			return Match{}, false, ErrCycle
		}
		visited[identity] = true
		row, found, err := readRedirect(ctx, store, executor, siteID, key)
		if err != nil {
			return Match{}, false, err
		}
		if !found && s.appendSlash {
			alternate, changed, err := appendSlashKey(key)
			if err != nil {
				return Match{}, false, ErrInvalid
			}
			if changed {
				row, found, err = readRedirect(ctx, store, executor, siteID, alternate)
				if err != nil {
					return Match{}, false, err
				}
			}
		}
		if !found {
			return first, hops > 0, nil
		}
		if rowsSeen[row.ID] {
			return Match{}, false, ErrCycle
		}
		if hops == s.maxHops {
			return Match{}, false, ErrLimit
		}
		rowsSeen[row.ID] = true
		current := Match{ID: row.ID, SiteID: row.SiteID, OldPath: row.OldPath, NewPath: row.NewPath, Permanent: row.Permanent}
		if row.NewPath == "" {
			if hops == 0 {
				first = current
			}
			return first, true, nil
		}
		target, err := parseTarget(row.NewPath, row.OldPath, origin, s.preserveQuery)
		if err != nil {
			return Match{}, false, ErrInvalid
		}
		current.Location, current.External = target.Location, target.External
		if hops == 0 {
			first = current
		}
		if target.External {
			return first, true, nil
		}
		key = target.Key
	}
}

func activeSite(ctx context.Context, store *orm.Store, executor db.Executor, siteID string) error {
	statement, args, err := orm.For(store, func() *sites.Site { return &sites.Site{} }).Filter(orm.Q("id", siteID)).Limit(2).SQLContext(ctx)
	if err != nil {
		return ErrUnavailable
	}
	var site sites.Site
	count, err := lookupRow(ctx, executor, statement, args, func(rows db.Rows) error {
		return rows.Scan(&site.ID, &site.Domain, &site.DisplayName, &site.Active)
	})
	if err != nil {
		return err
	}
	if count > 1 || count == 1 && site.ID != siteID {
		return ErrUnavailable
	}
	if count == 0 || !site.Active {
		return ErrSiteNotConfigured
	}
	return nil
}

func readRedirect(ctx context.Context, store *orm.Store, executor db.Executor, siteID, key string) (Redirect, bool, error) {
	statement, args, err := orm.For(store, func() *Redirect { return &Redirect{} }).Filter(orm.Q("site_id", siteID), orm.Q("old_path", key)).Limit(2).SQLContext(ctx)
	if err != nil {
		return Redirect{}, false, ErrUnavailable
	}
	var row Redirect
	count, err := lookupRow(ctx, executor, statement, args, func(rows db.Rows) error {
		return rows.Scan(&row.ID, &row.SiteID, &row.OldPath, &row.NewPath, &row.Permanent)
	})
	if err != nil || count > 1 {
		return Redirect{}, false, ErrUnavailable
	}
	if count == 0 {
		return Redirect{}, false, nil
	}
	id, idErr := redirectID(row.ID)
	oldPath, pathErr := parseOldPath(row.OldPath)
	newPath, targetErr := validateStoredTarget(row.NewPath)
	if idErr != nil || id != row.ID || row.SiteID != siteID || row.OldPath != key || pathErr != nil || oldPath != row.OldPath || targetErr != nil || newPath != row.NewPath {
		return Redirect{}, false, ErrInvalid
	}
	return row, true, nil
}

// lookupRow consumes at most two rows, detects ambiguous providers and checks
// every Rows terminal path including Close. The scan callback is private only.
func lookupRow(ctx context.Context, executor db.Executor, statement string, args []any, scan func(db.Rows) error) (count int, err error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	rows, queryErr := executor.Query(ctx, statement, args...)
	if !nilValue(rows) {
		defer func() {
			if closeErr := rows.Close(); closeErr != nil {
				count, err = 0, ErrUnavailable
			}
		}()
	}
	if queryErr != nil || nilValue(rows) {
		return 0, ErrUnavailable
	}
	if rows.Next() {
		count = 1
		if err := scan(rows); err != nil {
			return 0, ErrUnavailable
		}
		if rows.Next() {
			count = 2
		}
	}
	if rows.Err() != nil {
		return 0, ErrUnavailable
	}
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	return count, nil
}
