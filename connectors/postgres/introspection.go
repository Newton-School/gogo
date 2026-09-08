package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/db"
)

var _ db.CatalogIntrospector = Introspector{}

func catalogName(value string) bool {
	return value != "" && len(value) <= 63 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// InspectCatalog reads metadata only, on its own read-only repeatable-read
// transaction. Explicit schema names are bind values, not search_path changes.
func (Introspector) InspectCatalog(ctx context.Context, backend db.Backend, options db.CatalogOptions) (db.Catalog, error) {
	if catalogNil(ctx) || catalogNil(backend) || options.Schema != "" && !catalogName(options.Schema) || len(options.Relations) > 1000 {
		return db.Catalog{}, errors.New("postgres: invalid catalog inspection options")
	}
	names := append([]string(nil), options.Relations...)
	sort.Strings(names)
	for i, name := range names {
		if !catalogName(name) || i > 0 && name == names[i-1] {
			return db.Catalog{}, errors.New("postgres: invalid or duplicate inspection relation")
		}
	}
	if err := ctx.Err(); err != nil {
		return db.Catalog{}, err
	}
	tx, err := backend.BeginTx(ctx, db.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return db.Catalog{}, err
	}
	defer tx.Rollback()
	var namespace string
	if err := catalogRow(ctx, tx, "SELECT CASE WHEN $1::pg_catalog.text OPERATOR(pg_catalog.=) '' THEN pg_catalog.current_schema() ELSE $1 END", []any{options.Schema}, &namespace); err != nil {
		return db.Catalog{}, err
	}
	var exists bool
	if err := catalogRow(ctx, tx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname OPERATOR(pg_catalog.=) $1)", []any{namespace}, &exists); err != nil {
		return db.Catalog{}, err
	}
	if !exists {
		return db.Catalog{}, db.ErrNoRows
	}
	rows, err := tx.Query(ctx, `SELECT c.relname, CASE WHEN c.relispartition THEN 'partition' WHEN c.relkind OPERATOR(pg_catalog.=) 'p' THEN 'partitioned_table' WHEN c.relkind OPERATOR(pg_catalog.=) 'v' THEN 'view' WHEN c.relkind OPERATOR(pg_catalog.=) 'm' THEN 'materialized_view' ELSE 'table' END, COALESCE(pg_catalog.left(pg_catalog.obj_description(c.oid,'pg_class'),65537),''), COALESCE(t.spcname,'')
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid OPERATOR(pg_catalog.=) c.relnamespace LEFT JOIN pg_catalog.pg_tablespace t ON t.oid OPERATOR(pg_catalog.=) c.reltablespace
WHERE n.nspname OPERATOR(pg_catalog.=) $1 AND (c.relkind::pg_catalog.text  OPERATOR(pg_catalog.=)  ANY(ARRAY['r','p']::pg_catalog.text[]) OR $2 AND c.relkind::pg_catalog.text  OPERATOR(pg_catalog.=)  ANY(ARRAY['v','m']::pg_catalog.text[])) AND ($3::pg_catalog.text[] IS NULL OR c.relname OPERATOR(pg_catalog.=) ANY($3::pg_catalog.text[])) ORDER BY c.relname LIMIT 1001`, namespace, options.IncludeViews, names)
	if err != nil {
		return db.Catalog{}, err
	}
	var budget catalogBudget
	catalog := db.Catalog{Schema: namespace, Relations: []db.CatalogRelation{}}
	for rows.Next() {
		var relation db.CatalogRelation
		if err := rows.Scan(&relation.Name, &relation.Kind, &relation.Comment, &relation.Tablespace); err != nil {
			rows.Close()
			return db.Catalog{}, err
		}
		if err := budget.add(catalogTextLimit, relation.Name, relation.Kind, relation.Comment, relation.Tablespace); err != nil {
			rows.Close()
			return db.Catalog{}, err
		}
		catalog.Relations = append(catalog.Relations, relation)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return db.Catalog{}, err
	}
	if len(catalog.Relations) > 1000 {
		return db.Catalog{}, errors.New("postgres: select at most 1000 relations for inspection")
	}
	if len(names) != 0 && len(catalog.Relations) != len(names) {
		return db.Catalog{}, errors.New("postgres: selected inspection relation is missing or excluded")
	}
	for i := range catalog.Relations {
		relation := &catalog.Relations[i]
		if relation.Columns, err = inspectColumns(ctx, tx, namespace, relation.Name, &budget); err != nil {
			return db.Catalog{}, err
		}
		if relation.Constraints, err = inspectConstraints(ctx, tx, namespace, relation.Name, &budget); err != nil {
			return db.Catalog{}, err
		}
		if relation.Indexes, err = inspectIndexes(ctx, tx, namespace, relation.Name, &budget); err != nil {
			return db.Catalog{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return db.Catalog{}, err
	}
	if err := tx.Commit(); err != nil {
		return db.Catalog{}, err
	}
	if err := ctx.Err(); err != nil {
		return db.Catalog{}, err
	}
	return catalog, nil
}

// Unlike a convenience scalar query, inspection must account for close errors
// too: even its namespace and existence reads are part of the complete snapshot.
func catalogRow(ctx context.Context, executor db.Executor, query string, args []any, dest ...any) error {
	rows, err := executor.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		err = errors.Join(rows.Err(), rows.Close())
		if err == nil {
			err = db.ErrNoRows
		}
		return err
	}
	if err := rows.Scan(dest...); err != nil {
		return errors.Join(err, rows.Close())
	}
	if rows.Next() {
		return errors.Join(errors.New("postgres: unexpected scalar catalog row count"), rows.Close())
	}
	return errors.Join(rows.Err(), rows.Close())
}

func catalogList[T any](raw string) ([]T, error) {
	var values []T
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, errors.New("postgres: invalid catalog array metadata")
	}
	return values, nil
}

func inspectConstraints(ctx context.Context, executor db.Executor, schema, table string, budget *catalogBudget) ([]db.CatalogConstraint, error) {
	rows, err := executor.Query(ctx, `SELECT x.conname, x.contype::pg_catalog.text, pg_catalog.left(pg_catalog.pg_get_constraintdef(x.oid,false),65537), COALESCE(pg_catalog.left(pg_catalog.pg_get_expr(x.conbin,x.conrelid,false),65537),''),
COALESCE((SELECT pg_catalog.json_agg(a.attname ORDER BY k.ord)::pg_catalog.text FROM pg_catalog.unnest(x.conkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid OPERATOR(pg_catalog.=) x.conrelid AND a.attnum OPERATOR(pg_catalog.=) k.num),'[]'),
COALESCE(rn.nspname,''), COALESCE(rc.relname,''),
COALESCE((SELECT pg_catalog.json_agg(a.attname ORDER BY k.ord)::pg_catalog.text FROM pg_catalog.unnest(x.confkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid OPERATOR(pg_catalog.=) x.confrelid AND a.attnum OPERATOR(pg_catalog.=) k.num),'[]'),
x.confdeltype::pg_catalog.text, x.confupdtype::pg_catalog.text, x.condeferrable, x.condeferred, x.convalidated, x.connoinherit, x.confmatchtype::pg_catalog.text
FROM pg_catalog.pg_constraint x JOIN pg_catalog.pg_class c ON c.oid OPERATOR(pg_catalog.=) x.conrelid JOIN pg_catalog.pg_namespace n ON n.oid OPERATOR(pg_catalog.=) c.relnamespace
LEFT JOIN pg_catalog.pg_class rc ON rc.oid OPERATOR(pg_catalog.=) x.confrelid LEFT JOIN pg_catalog.pg_namespace rn ON rn.oid OPERATOR(pg_catalog.=) rc.relnamespace
WHERE n.nspname OPERATOR(pg_catalog.=) $1 AND c.relname OPERATOR(pg_catalog.=) $2 ORDER BY x.conname LIMIT 1025`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []db.CatalogConstraint{}
	for rows.Next() {
		var value db.CatalogConstraint
		var columns, referenced string
		var noInherit bool
		var matchType string
		if err := rows.Scan(&value.Name, &value.Kind, &value.Definition, &value.Expression, &columns, &value.ReferencedSchema, &value.ReferencedTable, &referenced, &value.OnDelete, &value.OnUpdate, &value.Deferrable, &value.InitiallyDeferred, &value.Validated, &noInherit, &matchType); err != nil {
			return nil, err
		}
		if len(result) >= 1024 {
			return nil, errors.New("postgres: inspect at most 1024 constraints per relation")
		}
		if err := budget.add(catalogTextLimit, value.Name, value.Kind, value.Definition, value.Expression, value.ReferencedSchema, value.ReferencedTable, value.OnDelete, value.OnUpdate); err != nil {
			return nil, err
		}
		if err := budget.add(catalogByteLimit, columns, referenced); err != nil {
			return nil, err
		}
		value.Columns, err = catalogList[string](columns)
		if err != nil {
			return nil, err
		}
		value.ReferencedColumns, err = catalogList[string](referenced)
		if err != nil {
			return nil, err
		}
		value.Kind = map[string]string{"p": "primary_key", "f": "foreign_key", "u": "unique", "c": "check", "x": "exclude", "n": "not_null"}[value.Kind]
		if value.Kind == "" {
			return nil, errors.New("postgres: unsupported catalog constraint kind")
		}
		if value.Kind == "check" && noInherit {
			value.MappingIssue = "CHECK NO INHERIT is not representable by the portable constraint descriptor"
		}
		if value.Kind == "foreign_key" && matchType != "s" {
			value.MappingIssue = "non-SIMPLE foreign-key matching requires an explicit mapping"
		}
		if err := budget.add(catalogTextLimit, value.MappingIssue); err != nil {
			return nil, err
		}
		actions := map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}
		value.OnDelete, value.OnUpdate = actions[value.OnDelete], actions[value.OnUpdate]
		result = append(result, value)
	}
	return result, errors.Join(rows.Err(), rows.Close())
}
