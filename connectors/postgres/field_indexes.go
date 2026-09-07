package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type fieldIndex struct{ name, table, column, marker string }

func implicitFieldIndex(schema models.Schema, field models.Field) (*fieldIndex, error) {
	if !field.DBIndex {
		return nil, nil
	}
	if !field.IsStored() {
		return nil, errors.New("postgres: DBIndex requires a stored column")
	}
	keys := schema.PKFields()
	if field.Unique || field.Kind == models.OneToOne || len(keys) == 1 && keys[0].Name == field.Name {
		return nil, nil
	}
	if field.Tablespace != "" || schema.Tablespace != "" {
		return nil, &db.Error{Code: db.UnsupportedFeature, Message: "Implicit field index tablespaces require explicit backend support"}
	}
	dialect := Dialect{}
	if _, err := dialect.QuoteIdentifier(schema.DBTable()); err != nil {
		return nil, err
	}
	if _, err := dialect.QuoteIdentifier(field.DBColumn()); err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(schema.DBTable() + "\x00" + field.DBColumn()))
	suffix := hex.EncodeToString(hash[:8]) + "_idx"
	prefix := schema.DBTable() + "_" + field.DBColumn()
	if len(prefix) > 63-len(suffix)-1 {
		prefix = prefix[:63-len(suffix)-1]
	}
	return &fieldIndex{name: prefix + "_" + suffix, table: schema.DBTable(), column: field.DBColumn(), marker: "gogo:field-index:v1:" + hex.EncodeToString(hash[:])}, nil
}

// An implicit name is never allowed to alias an explicitly declared resource.
// Cross-model table/index names are checked when a historical registry exists.
func (e SchemaEditor) fieldIndexes(schema models.Schema) ([]fieldIndex, error) {
	reserved := map[string]bool{schema.DBTable(): true}
	for _, other := range e.schemas {
		reserved[other.DBTable()] = true
		if other.Key() == schema.Key() {
			continue
		}
		for _, index := range other.Indexes {
			reserved[index.Name] = true
		}
		for _, constraint := range other.Constraints {
			reserved[constraint.Name] = true
		}
		if other.Unmanaged || other.Abstract || other.Proxy {
			continue
		}
		for _, field := range other.Fields {
			index, err := implicitFieldIndex(other, field)
			if err != nil {
				return nil, err
			}
			if index != nil {
				reserved[index.name] = true
			}
		}
	}
	for _, index := range schema.Indexes {
		reserved[index.Name] = true
	}
	for _, constraint := range schema.Constraints {
		reserved[constraint.Name] = true
	}
	indexes := []fieldIndex{}
	for _, field := range schema.Fields {
		index, err := implicitFieldIndex(schema, field)
		if err != nil {
			return nil, err
		}
		if index == nil {
			continue
		}
		if reserved[index.name] {
			return nil, errors.New("postgres: implicit field index name collides with a declared schema resource")
		}
		reserved[index.name] = true
		indexes = append(indexes, *index)
	}
	return indexes, nil
}

func quoteIndexLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func quoteIndexIdentifier(value string) string {
	quoted, _ := (Dialect{}).QuoteIdentifier(value)
	return quoted
}

func (index fieldIndex) createSQL() string {
	return "DO $gogo_field_index$ BEGIN " + index.createStatements() + "; END $gogo_field_index$"
}
func (index fieldIndex) createStatements() string {
	return "IF NOT EXISTS (SELECT 1 FROM pg_class tc JOIN pg_namespace ns ON ns.oid=tc.relnamespace WHERE ns.nspname=current_schema() AND tc.relname=" + quoteIndexLiteral(index.table) + " AND tc.oid=to_regclass(" + quoteIndexLiteral(quoteIndexIdentifier(index.table)) + ")) THEN RAISE EXCEPTION 'Implicit field index requires an unshadowed current-schema table' USING ERRCODE='55000'; END IF; " +
		"EXECUTE format('CREATE INDEX %I ON %I.%I (%I)', " + quoteIndexLiteral(index.name) + ", current_schema(), " + quoteIndexLiteral(index.table) + ", " + quoteIndexLiteral(index.column) + "); " + index.commentStatement()
}
func (index fieldIndex) commentStatement() string {
	return "EXECUTE format('COMMENT ON INDEX %I.%I IS %L', current_schema(), " + quoteIndexLiteral(index.name) + ", " + quoteIndexLiteral(index.marker) + ")"
}
func (index fieldIndex) dropStatement() string {
	return "EXECUTE format('DROP INDEX %I.%I', current_schema(), " + quoteIndexLiteral(index.name) + ")"
}
func (index fieldIndex) renameStatements(next fieldIndex) string {
	return "EXECUTE format('ALTER INDEX %I.%I RENAME TO %I', current_schema(), " + quoteIndexLiteral(index.name) + ", " + quoteIndexLiteral(next.name) + "); " + next.commentStatement()
}

// Schema migration ownership cannot be inferred from an old DBIndex flag: older
// framework versions did not create these indexes. Reject absent, foreign or
// structurally changed indexes before destructive operations. The table lock
// holds definition validation and its matching change in the same transaction.
func (index fieldIndex) guardedSQL(action string) string {
	return "DO $gogo_field_index$ BEGIN EXECUTE format('LOCK TABLE %I.%I IN ACCESS EXCLUSIVE MODE', current_schema(), " + quoteIndexLiteral(index.table) + "); IF NOT EXISTS (" +
		"SELECT 1 FROM pg_class ic JOIN pg_namespace ns ON ns.oid=ic.relnamespace JOIN pg_index ix ON ix.indexrelid=ic.oid JOIN pg_class tc ON tc.oid=ix.indrelid JOIN pg_attribute a ON a.attrelid=tc.oid AND a.attnum=ix.indkey[0] JOIN pg_am am ON am.oid=ic.relam JOIN pg_opclass op ON op.oid=ix.indclass[0] " +
		"WHERE ns.nspname=current_schema() AND tc.relnamespace=ns.oid AND ic.relname=" + quoteIndexLiteral(index.name) + " AND tc.relname=" + quoteIndexLiteral(index.table) + " AND tc.oid=to_regclass(" + quoteIndexLiteral(quoteIndexIdentifier(index.table)) + ") AND a.attname=" + quoteIndexLiteral(index.column) +
		" AND ic.relkind='i' AND am.amname='btree' AND op.opcdefault AND op.opcmethod=am.oid AND ix.indnatts=1 AND ix.indnkeyatts=1 AND NOT ix.indisunique AND NOT ix.indisprimary AND ix.indisvalid AND ix.indisready AND ix.indpred IS NULL AND ix.indexprs IS NULL AND ix.indoption[0]=0 AND ix.indcollation[0]=a.attcollation AND obj_description(ic.oid,'pg_class')=" + quoteIndexLiteral(index.marker) +
		") THEN RAISE EXCEPTION 'Implicit field index needs explicit reconciliation' USING ERRCODE='55000'; END IF; " + action + "; END $gogo_field_index$"
}
func replaceSchemaField(schema models.Schema, oldName string, field models.Field) models.Schema {
	schema = schema.Clone()
	for i, existing := range schema.Fields {
		if existing.Name == oldName {
			schema.Fields[i] = field
			return schema
		}
	}
	schema.Fields = append(schema.Fields, field)
	return schema
}

func sameRelationStorage(a, b *models.Relation) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Target == b.Target && slices.Equal(a.TargetFields, b.TargetFields) && a.NoConstraint == b.NoConstraint
}
func positiveKind(kind models.Kind) bool {
	return kind == models.PositiveSmallInteger || kind == models.PositiveInteger || kind == models.PositiveBigInteger
}
