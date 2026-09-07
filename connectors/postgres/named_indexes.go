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

type namedIndex struct {
	table, name, method, marker, sql string
	columns, include                 []string
	unique, nullsNotDistinct         bool
}

func (e SchemaEditor) AddIndex(ctx context.Context, executor db.Executor, schema models.Schema, index models.Index) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if schema.Unmanaged || schema.Proxy || schema.Abstract {
		return nil
	}
	definition, err := e.namedIndex(schema, index)
	if err != nil {
		return err
	}
	prospective := schema.Clone()
	present := false
	for _, declared := range prospective.Indexes {
		present = present || declared.Name == index.Name
	}
	if !present {
		prospective.Indexes = append(prospective.Indexes, index)
	}
	if _, err := e.fieldIndexes(prospective); err != nil {
		return err
	}
	if index.Concurrent {
		if _, transaction := executor.(db.Transaction); transaction {
			return &db.Error{Code: db.UnsupportedFeature, Message: "Concurrent index creation cannot run in a transaction"}
		}
		// Preserve the existing explicitly requested online creation path. Its
		// recovery/ownership is not silently upgraded to atomic lifecycle support.
		return e.addConcurrentIndex(ctx, executor, schema, index)
	}
	_, err = executor.Exec(ctx, "DO $gogo_named_index$ BEGIN "+definition.sql+"; "+definition.commentStatement()+"; END $gogo_named_index$")
	return err
}

func (e SchemaEditor) namedIndex(schema models.Schema, index models.Index) (namedIndex, error) {
	result := namedIndex{table: schema.DBTable(), name: index.Name, method: strings.ToLower(index.Method), unique: index.Unique}
	if result.method == "" {
		result.method = "btree"
	}
	name, err := e.Dialect.QuoteIdentifier(result.name)
	if err != nil {
		return result, err
	}
	if _, err := e.Dialect.QuoteIdentifier(result.table); err != nil {
		return result, err
	}
	switch result.method {
	case "btree", "hash", "gin", "gist", "spgist", "brin":
	default:
		return result, errors.New("postgres: unsupported index method")
	}
	if len(index.Fields) == 0 || index.Name == schema.DBTable() || strings.ContainsAny(index.Condition, ";\x00") || strings.Contains(index.Condition, "$gogo_named_index$") {
		return result, errors.New("postgres: invalid named index definition")
	}
	if index.NullsDistinct != nil && !index.Unique {
		return result, errors.New("postgres: NULLS DISTINCT requires a unique index")
	}
	result.nullsNotDistinct = index.Unique && index.NullsDistinct != nil && !*index.NullsDistinct
	resolve := func(names []string) ([]string, []string, error) {
		var columns, quoted []string
		for _, name := range names {
			field, exists := schema.Field(name)
			if !exists || !field.IsStored() {
				return nil, nil, errors.New("postgres: index requires stored model fields")
			}
			column, err := e.Dialect.QuoteIdentifier(field.DBColumn())
			if err != nil {
				return nil, nil, err
			}
			columns, quoted = append(columns, field.DBColumn()), append(quoted, column)
		}
		return columns, quoted, nil
	}
	var columns, included []string
	result.columns, columns, err = resolve(index.Fields)
	if err != nil {
		return result, err
	}
	result.include, included, err = resolve(index.Include)
	if err != nil {
		return result, err
	}
	// The physical descriptor does not change on a logical-only field rename.
	canonical := models.Index{Name: index.Name, Fields: result.columns, Include: result.include, Unique: index.Unique, Method: result.method, Condition: index.Condition}
	if index.Unique {
		value := !result.nullsNotDistinct
		canonical.NullsDistinct = &value
	}
	encoded, err := json.Marshal(struct {
		Table string
		Index models.Index
	}{result.table, canonical})
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(encoded)
	result.marker = "gogo:named-index:v1:" + hex.EncodeToString(digest[:])
	prefix := "CREATE "
	if index.Unique {
		prefix += "UNIQUE "
	}
	prefix += "INDEX "
	if index.Concurrent {
		prefix += "CONCURRENTLY "
	}
	// Qualified table rendering is deferred to the server so previews need no
	// catalog reads. Literal percent signs in trusted predicates are escaped.
	prefix += name + " ON %I.%I USING " + result.method + " (" + strings.Join(columns, ", ") + ")"
	if len(included) > 0 {
		prefix += " INCLUDE (" + strings.Join(included, ", ") + ")"
	}
	if result.nullsNotDistinct {
		prefix += " NULLS NOT DISTINCT"
	}
	if index.Condition != "" {
		prefix += " WHERE " + strings.ReplaceAll(index.Condition, "%", "%%")
	}
	result.sql = "EXECUTE format(" + quoteIndexLiteral(prefix) + ", current_schema(), " + quoteIndexLiteral(result.table) + ")"
	return result, nil
}

func (index namedIndex) objectOID() string {
	return "to_regclass(format('%I.%I', current_schema(), " + quoteIndexLiteral(index.name) + "))"
}
func (index namedIndex) commentStatement() string {
	return "EXECUTE format('COMMENT ON INDEX %I.%I IS %L', current_schema(), " + quoteIndexLiteral(index.name) + ", " + quoteIndexLiteral(index.marker+":") + " || md5(pg_get_indexdef(" + index.objectOID() + ")))"
}
func (index namedIndex) lockStatement() string {
	return "EXECUTE format('LOCK TABLE %I.%I IN ACCESS EXCLUSIVE MODE', current_schema(), " + quoteIndexLiteral(index.table) + ")"
}
func (index namedIndex) guardStatement() string {
	query := "SELECT 1 FROM pg_class ic JOIN pg_namespace ns ON ns.oid=ic.relnamespace JOIN pg_index ix ON ix.indexrelid=ic.oid JOIN pg_class tc ON tc.oid=ix.indrelid JOIN pg_am am ON am.oid=ic.relam WHERE ns.nspname=current_schema() AND tc.relnamespace=ns.oid AND ic.relname=" + quoteIndexLiteral(index.name) + " AND tc.relname=" + quoteIndexLiteral(index.table) +
		" AND tc.oid=to_regclass(" + quoteIndexLiteral(quoteIndexIdentifier(index.table)) + ") AND ic.relkind='i' AND am.amname=" + quoteIndexLiteral(index.method) +
		" AND ix.indnkeyatts=" + strconv.Itoa(len(index.columns)) + " AND ix.indnatts=" + strconv.Itoa(len(index.columns)+len(index.include)) +
		" AND ix.indisunique=" + strconv.FormatBool(index.unique) + " AND ix.indnullsnotdistinct=" + strconv.FormatBool(index.nullsNotDistinct) + " AND NOT ix.indisprimary AND ix.indisvalid AND ix.indisready AND ix.indexprs IS NULL AND NOT EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid=ic.oid AND con.conrelid=tc.oid AND con.contype IN ('p','u','x'))" +
		" AND obj_description(ic.oid,'pg_class')=" + quoteIndexLiteral(index.marker+":") + " || md5(pg_get_indexdef(ic.oid))"
	for position, column := range append(append([]string{}, index.columns...), index.include...) {
		i := strconv.Itoa(position)
		query += " AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid=tc.oid AND a.attname=" + quoteIndexLiteral(column) + " AND a.attnum=ix.indkey[" + i + "]"
		if position < len(index.columns) {
			query += " AND ix.indoption[" + i + "]=0 AND ix.indcollation[" + i + "]=a.attcollation AND EXISTS (SELECT 1 FROM pg_opclass op WHERE op.oid=ix.indclass[" + i + "] AND op.opcdefault AND op.opcmethod=am.oid)"
		}
		query += ")"
	}
	return "IF NOT EXISTS (" + query + ") THEN RAISE EXCEPTION 'Named index needs explicit reconciliation' USING ERRCODE='55000'; END IF"
}

func (e SchemaEditor) RemoveModelIndex(ctx context.Context, executor db.Executor, schema models.Schema, index models.Index) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if schema.Unmanaged || schema.Proxy || schema.Abstract {
		return nil
	}
	if index.Concurrent {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Concurrent index removal requires an explicit online migration"}
	}
	for _, constraint := range schema.Constraints {
		if constraint.Name == index.Name {
			return &db.Error{Code: db.UnsupportedFeature, Message: "Constraint indexes require explicit constraint operations"}
		}
	}
	definition, err := e.namedIndex(schema, index)
	if err != nil {
		return err
	}
	statement := "DO $gogo_named_index$ BEGIN " + definition.lockStatement() + "; " + definition.guardStatement() + "; EXECUTE format('DROP INDEX %I.%I', current_schema(), " + quoteIndexLiteral(index.Name) + "); END $gogo_named_index$"
	_, err = executor.Exec(ctx, statement)
	return err
}
