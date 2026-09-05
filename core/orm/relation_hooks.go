package orm

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/models"
)

func relationEvent(change RelationChange) RelationChange {
	change.Added = append([]models.Record(nil), change.Added...)
	change.Removed = append([]models.Record(nil), change.Removed...)
	change.Field = (models.Schema{Fields: []models.Field{change.Field}}).Clone().Fields[0]
	return change
}
func relationIdentities(change RelationChange) ([]string, error) {
	identities := []string{}
	for _, records := range [][]models.Record{{change.Source}, change.Added, change.Removed} {
		for _, record := range records {
			if record == nil {
				return nil, errors.New("orm: relation callback replaced an endpoint")
			}
			id, err := recordIdentity(record)
			if err != nil {
				return nil, err
			}
			identities = append(identities, id)
		}
	}
	return identities, nil
}

// checkChange keeps callback event slices private, rejects in-place identity
// mutation, and refreshes all endpoints after hooks before the final policy check.
func (m RelationManager) checkChange(ctx context.Context, binding relationBinding, change RelationChange) (RelationChange, error) {
	expected, err := relationIdentities(change)
	if err != nil {
		return change, err
	}
	check := func() error {
		actual, err := relationIdentities(change)
		if err != nil {
			return err
		}
		if len(actual) != len(expected) {
			return errors.New("orm: relation callback changed endpoint identities")
		}
		for i, id := range actual {
			if id != expected[i] {
				return errors.New("orm: relation callback changed endpoint identities")
			}
		}
		return nil
	}
	if m.Authorize != nil {
		if err := m.Authorize(ctx, relationEvent(change)); err != nil {
			return change, err
		}
		if err := check(); err != nil {
			return change, err
		}
	}
	for _, receiver := range m.BeforeChange {
		if err := receiver(ctx, relationEvent(change)); err != nil {
			return change, err
		}
		if err := check(); err != nil {
			return change, err
		}
	}
	change.Source, err = m.source(ctx, true)
	if err != nil {
		return change, err
	}
	for _, records := range [][]models.Record{change.Added, change.Removed} {
		for i, record := range records {
			records[i], err = m.scopedTarget(ctx, binding.target, record)
			if err != nil {
				return change, err
			}
		}
	}
	if m.Authorize != nil && len(m.BeforeChange) > 0 {
		if err := m.Authorize(ctx, relationEvent(change)); err != nil {
			return change, err
		}
		if err := check(); err != nil {
			return change, err
		}
	}
	return change, check()
}
