package gogo_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/api"
	"github.com/Newton-School/gogo/conf"
	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm"
	"github.com/Newton-School/gogo/orm/dialects/postgres"
	"github.com/Newton-School/gogo/queue"
)

func Example_referenceCoreAPIs() {
	settings := conf.DefaultSettings()
	settings.SecretKey = "dev-secret"
	settings.DatabaseURL = "sqlite:///tmp/gogo.sqlite3"

	meta := models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post", Fields: []models.FieldMeta{{Name: "id"}, {Name: "title"}}}
	compiled, _ := orm.NewCompiler(postgres.New()).CompileSelect(orm.NewQuery(meta).Select("id", "title"))

	serializer := api.NewSerializer(api.StringField("title", api.FieldOptions{Required: true}))
	_, _, valid := serializer.Validate(map[string]any{"title": "Hello"})

	app := queue.NewApp(queue.AppOptions{})
	_, _ = app.RegisterTask("blog.publish", func(context.Context, ...any) (any, error) { return "ok", nil }, queue.TaskOptions{})
	signature := queue.NewSignature("blog.publish", 1).WithQueue("default")

	fmt.Println(settings.Env)
	fmt.Println(meta.Label())
	fmt.Println(compiled.SQL)
	fmt.Println(valid)
	fmt.Println(signature.Name, signature.Options.Queue)
	// Output:
	// development
	// blog.Post
	// SELECT "id", "title" FROM "blog_post"
	// true
	// blog.publish default
}
