package flatpages

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"slices"
)

// Store owns immutable configuration. Backend and authorization services are
// trusted application code; their internal mutable state is not copied.
type Store struct{ state *storeState }
type storeState struct {
	backend   db.Backend
	alias     string
	maxSites  int
	authorize func(context.Context, Change) error
	registry  *models.Registry
}

func New(config Config) (store *Store, err error) {
	defer func() {
		if recover() != nil {
			store, err = nil, ErrConfiguration
		}
	}()
	if nilValue(config.Backend) || config.MaxSites < 0 || config.MaxSites > MaxSites {
		return nil, ErrConfiguration
	}
	if config.MaxSites == 0 {
		config.MaxSites = DefaultMaxSites
	}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{(&sites.Site{}).Schema(), (&FlatPage{}).Schema(), (&FlatPageSite{}).Schema()} {
		if err := registry.Register(schema); err != nil {
			return nil, ErrConfiguration
		}
	}
	if err := registry.Freeze(); err != nil {
		return nil, ErrConfiguration
	}
	alias := config.Backend.Alias()
	if alias == "" {
		return nil, ErrConfiguration
	}
	return &Store{state: &storeState{backend: config.Backend, alias: alias, maxSites: config.MaxSites, authorize: config.AuthorizeChange, registry: registry}}, nil
}

// Every operation pins its alias and retains the exact transaction it opened.
// A write also tracks actual Commit invocation, including a provider panic.
type operationBackend struct {
	db.Backend
	alias string
	tx    *operationTransaction
}

func (b *operationBackend) Alias() string { return b.alias }
func (b *operationBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil {
		if !nilValue(tx) {
			_ = tx.Rollback()
		}
		return nil, err
	}
	if nilValue(tx) {
		return nil, ErrUnavailable
	}
	b.tx = &operationTransaction{Transaction: tx}
	return b.tx, nil
}

type operationTransaction struct {
	db.Transaction
	commitAttempted, committed bool
	commitErr                  error
}

func (tx *operationTransaction) Commit() error {
	tx.commitAttempted = true
	tx.commitErr = tx.Transaction.Commit()
	tx.committed = tx.commitErr == nil
	return tx.commitErr
}
func (s storeState) operation() (*operationBackend, *orm.Store) {
	b := &operationBackend{Backend: s.backend, alias: s.alias}
	return b, orm.New(b, s.registry)
}

// SaveDomain is the authorized mutation boundary for a page and its complete
// site set. Empty ID creates; Create also permits a caller-known create-only ID.
// Otherwise a nonempty ID updates only. It never repairs drift
// left by privileged raw/model/M2M writes, and never retries uncertain outcomes.
func (s *Store) SaveDomain(ctx context.Context, input SaveInput) (saved Saved, err error) {
	if s == nil || s.state == nil {
		return Saved{}, ErrConfiguration
	}
	state := *s.state
	if state.authorize == nil {
		return Saved{}, ErrConfiguration
	}
	if len(input.SiteIDs) > state.maxSites {
		return Saved{}, ErrLimit
	}
	input.SiteIDs = slices.Clone(input.SiteIDs)
	var backend *operationBackend
	defer func() {
		panicValue := recover()
		if backend != nil && backend.tx != nil {
			if backend.tx.committed {
				err = nil
				return
			}
			if backend.tx.commitAttempted && (panicValue != nil || backend.tx.commitErr != nil) {
				if panicValue != nil || !knownRejectedCommit(backend.tx.commitErr) {
					saved, err = Saved{}, ErrOutcomeUnknown
					return
				}
			}
		}
		if panicValue != nil {
			err = ErrUnavailable
		}
		if canceled := contextError(ctx); canceled != nil {
			err = canceled
		}
		if err != nil {
			saved = Saved{}
		}
	}()
	if err := contextError(ctx); err != nil {
		return Saved{}, err
	}
	draft, err := validateDraft(input.Page)
	if err != nil {
		return Saved{}, err
	}
	create := input.Create || draft.ID == ""
	if draft.ID == "" {
		draft.ID, err = newPageID()
	} else {
		draft.ID, err = pageID(draft.ID)
	}
	if err != nil {
		return Saved{}, err
	}
	siteIDs, err := canonicalSites(input.SiteIDs)
	if err != nil {
		return Saved{}, err
	}
	after := draftInfo(draft)
	if db.InTransaction(ctx, state.alias) {
		return Saved{}, ErrTransaction
	}
	var store *orm.Store
	backend, store = state.operation()
	var outcome error
	err = db.Atomic(ctx, backend, db.AtomicOptions{Durable: true}, func(txctx context.Context) error {
		outcome = func() error {
			before, exists, e := readPage(txctx, store, backend.tx, after.ID, true)
			if e != nil {
				return e
			}
			if create && exists {
				return ErrConflict
			}
			if !create && !exists {
				return ErrNotFound
			}
			links, e := readLinks(txctx, store, backend.tx, orm.Q("page_id", after.ID), state.maxSites, true)
			if e != nil {
				return e
			}
			if e := validateMappings(links, after.ID, before.URL); e != nil {
				return e
			}
			if create && len(links) != 0 {
				return ErrUnavailable
			}
			union := siteUnion(links, siteIDs)
			siteValues, e := readSites(txctx, store, backend.tx, union)
			if e != nil {
				return e
			}
			previous := pageSnapshot{page: before, exists: exists, links: links, sites: siteValues}
			if e := state.authorizeSave(txctx, previous, after, siteIDs); e != nil {
				return e
			}
			// Finish all policy callbacks before beginning these callback-free
			// checks. External ACL state remains the policy owner's lock domain.
			if e := verifySnapshot(txctx, store, backend.tx, after.ID, union, previous, state.maxSites); e != nil {
				return e
			}
			expected, e := persistPage(txctx, store, after, siteIDs, links, create)
			if e != nil {
				return e
			}
			final := pageSnapshot{page: after, exists: true, links: expected, sites: siteValues}
			if e := verifySnapshot(txctx, store, backend.tx, after.ID, union, final, state.maxSites); e != nil {
				return e
			}
			if e := contextError(txctx); e != nil {
				return e
			}
			saved = Saved{Page: after, SiteIDs: slices.Clone(siteIDs)}
			return nil
		}()
		return outcome
	})
	if err != nil {
		if err != outcome {
			err = writeFailure(err)
		} else {
			err = domainFailure(err)
		}
	}
	return saved, err
}

type pageSnapshot struct {
	page   Info
	exists bool
	links  []linkInfo
	sites  []siteInfo
}

func canonicalSites(ids []string) ([]string, error) {
	for i, id := range ids {
		value, err := pageID(id)
		if err != nil {
			return nil, ErrInvalid
		}
		ids[i] = value
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}
func siteUnion(links []linkInfo, desired []string) []string {
	result := slices.Clone(desired)
	for _, link := range links {
		result = append(result, link.SiteID)
	}
	slices.Sort(result)
	return slices.Compact(result)
}
func validateMappings(links []linkInfo, id, url string) error {
	seenSites, seenIDs := map[string]bool{}, map[string]bool{}
	for _, link := range links {
		if link.PageID != id || link.URL != url || seenSites[link.SiteID] || seenIDs[link.ID] {
			return ErrUnavailable
		}
		seenSites[link.SiteID], seenIDs[link.ID] = true, true
	}
	return nil
}
func readSites(ctx context.Context, store *orm.Store, executor db.Executor, ids []string) ([]siteInfo, error) {
	values := make([]siteInfo, 0, len(ids))
	for _, id := range ids {
		value, found, err := readSite(ctx, store, executor, id, true)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrSiteNotConfigured
		}
		values = append(values, value)
	}
	return values, nil
}
func (s storeState) authorizeSave(ctx context.Context, before pageSnapshot, after Info, desired []string) error {
	invoke := func(action ChangeAction, siteID string) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		change := Change{Action: action, After: after, SiteID: siteID}
		if before.exists {
			copy := before.page
			change.Before = &copy
		}
		err := s.authorize(ctx, change)
		if canceled := contextError(ctx); canceled != nil {
			return canceled
		}
		if err == ErrForbidden {
			return ErrForbidden
		}
		if err != nil {
			return ErrUnavailable
		}
		return nil
	}
	action := CreatePage
	if before.exists {
		action = UpdatePage
	}
	if err := invoke(action, ""); err != nil {
		return err
	}
	current, target := map[string]bool{}, map[string]bool{}
	for _, link := range before.links {
		current[link.SiteID] = true
	}
	for _, id := range desired {
		target[id] = true
	}
	for _, id := range siteUnion(before.links, desired) {
		action := RetainSite
		if !current[id] {
			action = AttachSite
		} else if !target[id] {
			action = DetachSite
		}
		if err := invoke(action, id); err != nil {
			return err
		}
	}
	return nil
}
func verifySnapshot(ctx context.Context, store *orm.Store, executor db.Executor, id string, siteIDs []string, expected pageSnapshot, maximum int) error {
	page, exists, err := readPage(ctx, store, executor, id, true)
	if err != nil {
		return err
	}
	links, err := readLinks(ctx, store, executor, orm.Q("page_id", id), maximum, true)
	if err != nil {
		return err
	}
	siteValues, err := readSites(ctx, store, executor, siteIDs)
	if err != nil {
		return err
	}
	if exists != expected.exists || page != expected.page || !slices.Equal(links, expected.links) || !slices.Equal(siteValues, expected.sites) {
		return ErrUnavailable
	}
	return contextError(ctx)
}
func persistPage(ctx context.Context, store *orm.Store, after Info, desired []string, before []linkInfo, create bool) ([]linkInfo, error) {
	page := infoModel(after)
	if err := store.Save(ctx, page, orm.SaveOptions{ForceInsert: create, ForceUpdate: !create}); err != nil {
		return nil, err
	}
	previous := map[string]linkInfo{}
	target := map[string]bool{}
	for _, link := range before {
		previous[link.SiteID] = link
	}
	for _, id := range desired {
		target[id] = true
	}
	for _, link := range before {
		if target[link.SiteID] {
			continue
		}
		model := &FlatPageSite{ID: link.ID, PageID: link.PageID, SiteID: link.SiteID, URL: link.URL}
		counts, err := store.Delete(ctx, model)
		if err != nil {
			return nil, err
		}
		if counts[model.Schema().Key()] != 1 {
			return nil, ErrUnavailable
		}
	}
	result := make([]linkInfo, 0, len(desired))
	for _, id := range desired {
		link, exists := previous[id]
		if !exists {
			var err error
			link.ID, err = newPageID()
			if err != nil {
				return nil, err
			}
			link.PageID, link.SiteID = after.ID, id
		}
		if !exists || link.URL != after.URL {
			link.URL = after.URL
			model := &FlatPageSite{ID: link.ID, PageID: link.PageID, SiteID: link.SiteID, URL: link.URL}
			if err := store.Save(ctx, model, orm.SaveOptions{ForceInsert: !exists, ForceUpdate: exists}); err != nil {
				return nil, err
			}
		}
		result = append(result, link)
	}
	return result, nil
}
func domainFailure(err error) error {
	switch err {
	case ErrConfiguration, ErrInvalid, ErrUnavailable, ErrNotFound, ErrSiteNotConfigured, ErrConflict, ErrForbidden, ErrTransaction, ErrLimit:
		return err
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return writeFailure(err)
}
func writeFailure(err error) error {
	budget := 128
	if onlyMappingConflict(err, 0, &budget) {
		return ErrConflict
	}
	return ErrUnavailable
}
func onlyMappingConflict(err error, depth int, budget *int) bool {
	*budget--
	if err == nil || depth > 16 || *budget < 0 {
		return false
	}
	if value, ok := err.(*db.Error); ok {
		return value != nil && value.Cause == nil && value.Code == db.UniqueViolation && (value.Constraint == SiteURLConstraint || value.Constraint == PageSiteConstraint)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 || len(children) > 16 {
			return false
		}
		for _, child := range children {
			if !onlyMappingConflict(child, depth+1, budget) {
				return false
			}
		}
		return true
	}
	// Unknown wrapper types may carry additional operational state. A mixed
	// cleanup failure is never reported as an ordinary uniqueness conflict.
	return false
}
func knownRejectedCommit(err error) bool {
	value, ok := err.(*db.Error)
	if !ok || value == nil || value.Cause != nil {
		return false
	}
	for _, code := range []db.ErrorCode{db.UniqueViolation, db.ForeignKeyViolation, db.CheckViolation, db.NotNullViolation, db.SerializationFailure, db.Deadlock} {
		if value.Code == code {
			return true
		}
	}
	return false
}
