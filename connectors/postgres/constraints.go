package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type modelConstraint struct {
	table, name, kind, sql, marker string
	columns                        []string
	deferred, nullsNotDistinct     bool
	partial                        *namedIndex
}

func (e SchemaEditor) modelConstraint(schema models.Schema, value models.Constraint) (modelConstraint, error) {
	result := modelConstraint{table: schema.DBTable(), name: value.Name, kind: strings.ToLower(value.Kind), deferred: value.Deferrable}
	candidate := schema.Clone()
	candidate.Constraints = []models.Constraint{value}
	if err := candidate.Validate(); err != nil {
		return result, err
	}
	if _, err := e.Dialect.QuoteIdentifier(result.table); err != nil {
		return result, err
	}
	canonical := (models.Schema{Constraints: []models.Constraint{value}}).Clone().Constraints[0]
	canonical.Kind = result.kind
	for i, name := range value.Fields {
		field, exists := schema.Field(name)
		if !exists || !field.IsStored() {
			return result, errors.New("postgres: constraint requires stored model fields")
		}
		if _, err := e.Dialect.QuoteIdentifier(field.DBColumn()); err != nil {
			return result, err
		}
		canonical.Fields[i] = field.DBColumn()
		result.columns = append(result.columns, field.DBColumn())
	}
	if result.kind == "unique" {
		result.nullsNotDistinct = value.NullsDistinct != nil && !*value.NullsDistinct
		distinct := !result.nullsNotDistinct
		canonical.NullsDistinct = &distinct
	}
	if result.kind == "unique" && value.Condition != "" {
		index, err := e.namedIndex(schema, models.Index{Name: value.Name, Fields: value.Fields, Unique: true, Condition: value.Condition, NullsDistinct: value.NullsDistinct})
		if err != nil {
			return result, err
		}
		result.partial = &index
		return result, nil
	}
	sql, err := e.constraint(schema, value)
	if err != nil {
		return result, err
	}
	result.sql = sql
	encoded, err := json.Marshal(struct {
		Table      string
		Constraint models.Constraint
	}{result.table, canonical})
	if err != nil {
		return result, err
	}
	hash := sha256.Sum256(encoded)
	result.marker = "gogo:constraint:v1:" + hex.EncodeToString(hash[:])
	return result, nil
}

func constraintBlock(statements ...string) string {
	// A quoted block body cannot be terminated by dollar delimiters occurring
	// inside an application's trusted expression or identifier literals.
	return "DO " + quoteIndexLiteral("BEGIN "+strings.Join(statements, "; ")+"; END")
}

func (value modelConstraint) objectOID() string {
	return "(SELECT c.oid FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=current_schema() AND t.relname=" + quoteIndexLiteral(value.table) + " AND c.conname=" + quoteIndexLiteral(value.name) + ")"
}

func (value modelConstraint) commentStatement() string {
	return "EXECUTE format('COMMENT ON CONSTRAINT %I ON %I.%I IS %L', " + quoteIndexLiteral(value.name) + ", current_schema(), " + quoteIndexLiteral(value.table) + ", " + quoteIndexLiteral(value.marker+":") + " || md5(pg_get_constraintdef(" + value.objectOID() + ")))"
}

func (value modelConstraint) lockStatement() string {
	return "EXECUTE format('LOCK TABLE %I.%I IN ACCESS EXCLUSIVE MODE', current_schema(), " + quoteIndexLiteral(value.table) + ")"
}

func (value modelConstraint) guardStatement() string {
	typeCode := "c"
	if value.kind == "unique" {
		typeCode = "u"
	}
	query := "SELECT 1 FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=current_schema() AND c.connamespace=n.oid AND t.relname=" + quoteIndexLiteral(value.table) +
		" AND t.oid=to_regclass(" + quoteIndexLiteral(quoteIndexIdentifier(value.table)) + ") AND c.conname=" + quoteIndexLiteral(value.name) + " AND c.contype=" + quoteIndexLiteral(typeCode) +
		" AND c.convalidated AND c.connoinherit=" + strconv.FormatBool(value.kind == "unique") + " AND c.conislocal AND c.coninhcount=0 AND c.condeferrable=" + strconv.FormatBool(value.deferred) + " AND c.condeferred=" + strconv.FormatBool(value.deferred) +
		" AND obj_description(c.oid,'pg_constraint')=" + quoteIndexLiteral(value.marker+":") + " || md5(pg_get_constraintdef(c.oid))"
	if value.kind == "unique" {
		query += " AND EXISTS (SELECT 1 FROM pg_index ix JOIN pg_class ic ON ic.oid=ix.indexrelid JOIN pg_am am ON am.oid=ic.relam WHERE ix.indexrelid=c.conindid AND ix.indrelid=t.oid AND ic.relnamespace=n.oid AND ic.relkind='i' AND am.amname='btree' AND ix.indisunique AND NOT ix.indisprimary AND ix.indisvalid AND ix.indisready AND ix.indimmediate=" + strconv.FormatBool(!value.deferred) +
			" AND ix.indnullsnotdistinct=" + strconv.FormatBool(value.nullsNotDistinct) + " AND ix.indnkeyatts=" + strconv.Itoa(len(value.columns)) + " AND ix.indnatts=" + strconv.Itoa(len(value.columns)) + " AND ix.indpred IS NULL AND ix.indexprs IS NULL"
		for i, column := range value.columns {
			position := strconv.Itoa(i)
			query += " AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid=t.oid AND a.attname=" + quoteIndexLiteral(column) + " AND a.attnum=ix.indkey[" + position + "] AND a.attnum=c.conkey[" + strconv.Itoa(i+1) + "] AND ix.indoption[" + position + "]=0 AND ix.indcollation[" + position + "]=a.attcollation AND EXISTS (SELECT 1 FROM pg_opclass op WHERE op.oid=ix.indclass[" + position + "] AND op.opcdefault AND op.opcmethod=am.oid))"
		}
		query += ")"
	} else {
		query += " AND c.conbin IS NOT NULL AND c.conindid=0"
	}
	return "IF NOT EXISTS (" + query + ") THEN RAISE EXCEPTION 'Constraint needs explicit reconciliation' USING ERRCODE='55000'; END IF"
}

func (e SchemaEditor) AddConstraint(ctx context.Context, executor db.Executor, schema models.Schema, constraint models.Constraint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if schema.Unmanaged || schema.Proxy || schema.Abstract {
		return nil
	}
	definition, err := e.modelConstraint(schema, constraint)
	if err != nil {
		return err
	}
	if definition.partial != nil {
		return e.AddIndex(ctx, executor, schema, models.Index{Name: constraint.Name, Fields: constraint.Fields, Unique: true, Condition: constraint.Condition, NullsDistinct: constraint.NullsDistinct})
	}
	prospective := schema.Clone()
	present := false
	for i, value := range prospective.Constraints {
		if value.Name == constraint.Name {
			prospective.Constraints[i], present = constraint, true
		}
	}
	if !present {
		prospective.Constraints = append(prospective.Constraints, constraint)
	}
	if _, err := e.fieldIndexes(prospective); err != nil {
		return err
	}
	create := "EXECUTE format(" + quoteIndexLiteral("ALTER TABLE %I.%I ADD "+strings.ReplaceAll(definition.sql, "%", "%%")) + ", current_schema(), " + quoteIndexLiteral(definition.table) + ")"
	_, err = executor.Exec(ctx, constraintBlock(create, definition.commentStatement()))
	return err
}

func (e SchemaEditor) RemoveConstraint(ctx context.Context, executor db.Executor, schema models.Schema, constraint models.Constraint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if schema.Unmanaged || schema.Proxy || schema.Abstract {
		return nil
	}
	definition, err := e.modelConstraint(schema, constraint)
	if err != nil {
		return err
	}
	if definition.partial != nil {
		index := *definition.partial
		_, err = executor.Exec(ctx, constraintBlock(index.lockStatement(), index.guardStatement(), "EXECUTE format('DROP INDEX %I.%I', current_schema(), "+quoteIndexLiteral(index.name)+")"))
		return err
	}
	drop := "EXECUTE format('ALTER TABLE %I.%I DROP CONSTRAINT %I', current_schema(), " + quoteIndexLiteral(definition.table) + ", " + quoteIndexLiteral(definition.name) + ")"
	_, err = executor.Exec(ctx, constraintBlock(definition.lockStatement(), definition.guardStatement(), drop))
	return err
}
