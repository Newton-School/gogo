# Quickstart Tutorial

This tutorial builds a small project, creates an app, defines a model, creates migrations, registers admin, exposes an API, and runs the server.

## 1. Create A Project

```bash
gogo startproject mysite
cd mysite
```

Set required environment values:

```bash
cp .env.example .env
printf 'GOGO_SECRET_KEY=dev-secret\nDATABASE_URL=sqlite://./db.sqlite3\n' >> .env
```

## 2. Create An App

```bash
go run manage.go startapp blog
```

Add the app to installed apps:

```go
settings := conf.Settings{
	InstalledApps: []string{
		"gogo.contrib.sites",
		"gogo.contrib.humanize",
		"blog",
	},
}
_ = settings
```

## 3. Define A Model

Use `models.Metadata` as the source of truth:

```go
package blog

import "github.com/Newton-School/gogo/models"

type Post struct {
	models.BaseModel
	Title string
	Body  string
}

func (Post) ModelMeta() models.Metadata {
	return models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
			{Name: "body", Column: "body"},
		},
		DefaultPermissions: []string{"add", "change", "delete", "view"},
	}
}
```

## 4. Create And Apply Migrations

```bash
go run manage.go makemigrations blog
go run manage.go migrate
```

Review generated SQL during development:

```bash
go run manage.go sqlmigrate blog 0001_initial
```

## 5. Create A Staff User

```bash
go run manage.go createsuperuser
```

Use `go run manage.go changepassword` later for password rotation.

## 6. Register Admin

```go
package blog

import "github.com/Newton-School/gogo/admin"

func RegisterAdmin(registry *admin.Registry) error {
	return registry.Register(Post{}, admin.ModelAdmin{
		ListDisplay:  []string{"id", "title"},
		SearchFields: []string{"title", "body"},
	})
}
```

The key extension point is `admin.ModelAdmin`.

## 7. Add An API

```go
package blog

import (
	"context"

	"github.com/Newton-School/gogo/api"
)

func RegisterAPI(router *api.Router) error {
	serializer := api.NewSerializer(
		api.IntegerField("id", api.FieldOptions{ReadOnly: true}),
		api.StringField("title", api.FieldOptions{Required: true}),
		api.StringField("body", api.FieldOptions{Required: true}),
	)
	_ = serializer
	return router.Handle("post-list", "/api/posts/", func(context.Context, *api.Request) api.Response {
		return api.JSON(200, map[string]any{"results": []any{}})
	}, "GET")
}
```

For ViewSets, start with `api.NewRouter` and register `api.ModelViewSet`.

## 8. Run The Server

```bash
go run manage.go check
go run manage.go runserver :8000
```

Open the admin at `/admin/` and the API route at `/api/posts/`.
