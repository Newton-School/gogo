package api

import (
	"context"
	"errors"

	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm"
)

// MetadataViewSetStore adapts an ORM metadata store to ModelViewSetStore.
type MetadataViewSetStore struct {
	Store *orm.MetadataStore
	Model models.Metadata
}

// NewMetadataViewSetStore creates a model-backed API viewset store.
func NewMetadataViewSetStore(store *orm.MetadataStore, meta models.Metadata) MetadataViewSetStore {
	return MetadataViewSetStore{Store: store, Model: meta.Clone()}
}

func (s MetadataViewSetStore) List(ctx context.Context, _ *Request) ([]map[string]any, error) {
	if s.Store == nil {
		return nil, ErrInternal
	}
	return s.Store.List(ctx, s.Model)
}

func (s MetadataViewSetStore) Retrieve(ctx context.Context, _ *Request, lookup string) (map[string]any, error) {
	if s.Store == nil {
		return nil, ErrInternal
	}
	row, ok, err := s.Store.Get(ctx, s.Model, lookup)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return row, nil
}

func (s MetadataViewSetStore) Create(ctx context.Context, _ *Request, data map[string]any) (map[string]any, error) {
	if s.Store == nil {
		return nil, ErrInternal
	}
	return s.Store.Create(ctx, s.Model, data)
}

func (s MetadataViewSetStore) Update(ctx context.Context, _ *Request, lookup string, data map[string]any, partial bool) (map[string]any, error) {
	if s.Store == nil {
		return nil, ErrInternal
	}
	row, err := s.Store.Update(ctx, s.Model, lookup, data, partial)
	if errors.Is(err, orm.ErrDoesNotExist) {
		return nil, ErrNotFound
	}
	return row, err
}

func (s MetadataViewSetStore) Destroy(ctx context.Context, _ *Request, lookup string) error {
	if s.Store == nil {
		return ErrInternal
	}
	if err := s.Store.Delete(ctx, s.Model, lookup); errors.Is(err, orm.ErrDoesNotExist) {
		return ErrNotFound
	} else {
		return err
	}
}
