package orm

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func (s *Store) ValidateRelation(ctx context.Context, record models.Record, field models.Field) error {
	if field.Relation == nil || s.Registry == nil {
		return errors.New("orm: relation validation requires complete model registry")
	}
	target, ok := s.Registry.Get(field.Relation.Target)
	if !ok {
		return errors.New("orm: unknown relation target")
	}
	keys := field.Relation.TargetFields
	if len(keys) == 0 {
		for _, pk := range target.PKFields() {
			keys = append(keys, pk.Name)
		}
	}
	if len(keys) != 1 {
		return errors.New("orm: scalar relation requires one target field")
	}
	key, ok := target.Field(keys[0])
	if !ok {
		return errors.New("orm: relation target field is missing")
	}
	value, err := record.Get(field.Name)
	if err != nil {
		return err
	}
	if value == nil {
		return nil
	}
	value, err = encodeField(key, value)
	if err != nil {
		return err
	}
	table, err := s.Backend.Dialect().QuoteIdentifier(target.DBTable())
	if err != nil {
		return err
	}
	column, err := s.Backend.Dialect().QuoteIdentifier(key.DBColumn())
	if err != nil {
		return err
	}
	var exists bool
	if err := db.QueryRow(ctx, db.ExecutorFor(ctx, s.Backend), "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE "+column+"="+s.Backend.Dialect().Placeholder(1)+")", []any{value}, &exists); err != nil {
		return err
	}
	if !exists {
		return models.Invalid("invalid_choice", "Related object does not exist.")
	}
	return nil
}
