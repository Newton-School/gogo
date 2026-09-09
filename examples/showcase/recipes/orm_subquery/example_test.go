package orm_test

import (
	"context"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// This compile-only example injects an already configured backend. The caller
// owns its lifecycle, migrations, current tenant and permission checks.
func Example_orm_subquerySubquery() {
	build := func(backend db.Backend, tenant int64) orm.Query[*models.MapRecord] {
		article := models.Schema{AppLabel: "blog", Name: "Article", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("tenant")}}
		comment := models.Schema{AppLabel: "blog", Name: "Comment", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("article_id"), models.BigIntegerField("tenant"), models.TextField("body")}}
		store := orm.New(backend, nil)
		scope := func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", tenant), nil }
		articles := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(article); return record }).WithScope(scope)
		comments := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(comment); return record }).WithScope(scope)
		inner := comments.Filter(orm.Q("article_id", orm.OuterRef("id"))).OrderBy("-id")
		return articles.Annotate(map[string]orm.ResultExpression{
			"latest_body":  orm.Typed(orm.Subquery(inner.Limit(1), "body"), models.TextField("latest_body", models.Nullable, models.Optional)),
			"has_comments": orm.Typed(orm.Exists(inner), models.BooleanField("has_comments")),
		})
	}
	_ = build // Calling build constructs a query; All(ctx) performs the read.
}
