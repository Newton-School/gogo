package postgres

import (
	"context"
	"errors"

	"github.com/Newton-School/gogo/core/db"
)

func inspectIndexes(ctx context.Context, executor db.Executor, schema, table string, budget *catalogBudget) ([]db.CatalogIndex, error) {
	rows, err := executor.Query(ctx, `SELECT ic.relname, am.amname, pg_catalog.left(pg_catalog.pg_get_indexdef(i.indexrelid,0,false),65537), COALESCE(pg_catalog.left(pg_catalog.pg_get_expr(i.indpred,i.indrelid,false),65537),''), COALESCE(con.conname,''),
COALESCE((SELECT pg_catalog.json_agg(COALESCE(a.attname,'') ORDER BY k.ord)::pg_catalog.text FROM pg_catalog.unnest(i.indkey) WITH ORDINALITY k(num,ord) LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid OPERATOR(pg_catalog.=) i.indrelid AND a.attnum OPERATOR(pg_catalog.=) k.num WHERE k.ord OPERATOR(pg_catalog.<=) i.indnkeyatts),'[]'),
COALESCE((SELECT pg_catalog.json_agg(CASE WHEN k.num OPERATOR(pg_catalog.=) 0 THEN pg_catalog.left(pg_catalog.pg_get_indexdef(i.indexrelid,k.ord::pg_catalog.int4,false),65537) ELSE '' END ORDER BY k.ord)::pg_catalog.text FROM pg_catalog.unnest(i.indkey) WITH ORDINALITY k(num,ord) WHERE k.ord OPERATOR(pg_catalog.<=) i.indnkeyatts),'[]'),
COALESCE((SELECT pg_catalog.json_agg(a.attname ORDER BY k.ord)::pg_catalog.text FROM pg_catalog.unnest(i.indkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid OPERATOR(pg_catalog.=) i.indrelid AND a.attnum OPERATOR(pg_catalog.=) k.num WHERE k.ord OPERATOR(pg_catalog.>) i.indnkeyatts),'[]'),
COALESCE((SELECT pg_catalog.json_agg(pg_catalog.format('%I.%I',n.nspname,op.opcname) ORDER BY k.ord)::pg_catalog.text FROM pg_catalog.unnest(i.indclass) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_opclass op ON op.oid OPERATOR(pg_catalog.=) k.num JOIN pg_catalog.pg_namespace n ON n.oid OPERATOR(pg_catalog.=) op.opcnamespace),'[]'),
COALESCE((SELECT pg_catalog.json_agg(op.opcdefault ORDER BY k.ord)::pg_catalog.text FROM pg_catalog.unnest(i.indclass) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_opclass op ON op.oid OPERATOR(pg_catalog.=) k.num),'[]'),
COALESCE((SELECT pg_catalog.json_agg(CASE WHEN co.oid IS NULL THEN '' ELSE pg_catalog.format('%I.%I',n.nspname,co.collname) END ORDER BY k.ord)::pg_catalog.text FROM pg_catalog.unnest(i.indcollation) WITH ORDINALITY k(num,ord) LEFT JOIN pg_catalog.pg_collation co ON co.oid OPERATOR(pg_catalog.=) k.num LEFT JOIN pg_catalog.pg_namespace n ON n.oid OPERATOR(pg_catalog.=) co.collnamespace),'[]'),
pg_catalog.array_to_json(i.indoption::pg_catalog.int2[])::pg_catalog.text, i.indisunique, i.indisprimary, i.indisvalid, i.indisready, NOT i.indnullsnotdistinct, COALESCE(pg_catalog.array_length(ic.reloptions,1),0) OPERATOR(pg_catalog.>) 0, ic.reltablespace OPERATOR(pg_catalog.<>) 0
FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid OPERATOR(pg_catalog.=) i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid OPERATOR(pg_catalog.=) c.relnamespace
JOIN pg_catalog.pg_class ic ON ic.oid OPERATOR(pg_catalog.=) i.indexrelid JOIN pg_catalog.pg_am am ON am.oid OPERATOR(pg_catalog.=) ic.relam LEFT JOIN pg_catalog.pg_constraint con ON con.conindid OPERATOR(pg_catalog.=) i.indexrelid AND con.contype::pg_catalog.text  OPERATOR(pg_catalog.=)  ANY(ARRAY['p','u','x']::pg_catalog.text[])
WHERE n.nspname OPERATOR(pg_catalog.=) $1 AND c.relname OPERATOR(pg_catalog.=) $2 ORDER BY ic.relname LIMIT 1025`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []db.CatalogIndex{}
	for rows.Next() {
		var index db.CatalogIndex
		var columns, expressions, include, opclasses, defaults, collations, options string
		var storageOptions, tablespace bool
		if err := rows.Scan(&index.Name, &index.Method, &index.Definition, &index.Condition, &index.Constraint, &columns, &expressions, &include, &opclasses, &defaults, &collations, &options, &index.Unique, &index.Primary, &index.Valid, &index.Ready, &index.NullsDistinct, &storageOptions, &tablespace); err != nil {
			return nil, err
		}
		if storageOptions || tablespace {
			index.MappingIssue = "index storage parameters or explicit tablespace require an explicit mapping"
		}
		if err := budget.add(catalogTextLimit, index.MappingIssue); err != nil {
			return nil, err
		}
		if len(result) >= 1024 {
			return nil, errors.New("postgres: inspect at most 1024 indexes per relation")
		}
		if err := budget.add(catalogTextLimit, index.Name, index.Method, index.Definition, index.Condition, index.Constraint); err != nil {
			return nil, err
		}
		if err := budget.add(catalogByteLimit, columns, expressions, include, opclasses, defaults, collations, options); err != nil {
			return nil, err
		}
		for _, item := range []struct {
			raw string
			to  *[]string
		}{{columns, &index.Columns}, {expressions, &index.Expressions}, {include, &index.Include}, {opclasses, &index.OpClasses}, {collations, &index.Collations}} {
			*item.to, err = catalogList[string](item.raw)
			if err != nil {
				return nil, err
			}
		}
		index.DefaultOpClasses, err = catalogList[bool](defaults)
		if err != nil {
			return nil, err
		}
		index.Options, err = catalogList[int](options)
		if err != nil {
			return nil, err
		}
		result = append(result, index)
	}
	return result, errors.Join(rows.Err(), rows.Close())
}
