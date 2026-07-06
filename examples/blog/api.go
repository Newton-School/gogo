package blog

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Newton-School/gogo/api"
)

func AuthorSerializer() *api.Serializer {
	return api.NewSerializer(
		api.IntegerField("id", api.FieldOptions{ReadOnly: true}),
		api.StringField("display_name", api.FieldOptions{Required: true}),
		api.StringField("bio", api.FieldOptions{AllowBlank: true}),
		api.URLField("website", api.FieldOptions{AllowBlank: true}),
	)
}

func PostSerializer() *api.Serializer {
	return api.NewSerializer(
		api.IntegerField("id", api.FieldOptions{ReadOnly: true}),
		api.PrimaryKeyRelatedField("author_id", api.FieldOptions{Required: true, Source: "author"}),
		api.StringField("title", api.FieldOptions{Required: true}),
		api.SlugField("slug", api.FieldOptions{Required: true}),
		api.StringField("body", api.FieldOptions{Required: true}),
		api.ChoiceField("status", api.FieldOptions{Required: true}, []string{"draft", "published", "archived"}),
		api.DateTimeField("published_at", api.FieldOptions{AllowNull: true}),
		api.ListField("tags", api.FieldOptions{}, api.SlugRelatedField("tag", api.FieldOptions{})),
		api.MethodField("absolute_url", func(obj map[string]any) any {
			return fmt.Sprintf("/blog/%v/", obj["slug"])
		}),
	)
}

func CommentSerializer() *api.Serializer {
	return api.NewSerializer(
		api.IntegerField("id", api.FieldOptions{ReadOnly: true}),
		api.PrimaryKeyRelatedField("post_id", api.FieldOptions{Required: true, Source: "post"}),
		api.StringField("name", api.FieldOptions{Required: true}),
		api.EmailField("email", api.FieldOptions{Required: true, WriteOnly: true}),
		api.StringField("body", api.FieldOptions{Required: true}),
		api.ChoiceField("status", api.FieldOptions{ReadOnly: true, Default: "pending"}, []string{"pending", "approved", "spam"}),
	)
}

func RegisterAPI(router *api.Router) error {
	if router == nil {
		return fmt.Errorf("blog API router is required")
	}
	routes := []struct {
		name    string
		pattern string
		view    api.View
		methods []string
	}{
		{name: "blog-post-list", pattern: "posts", view: listPosts, methods: []string{http.MethodGet}},
		{name: "blog-post-create", pattern: "posts", view: createPost, methods: []string{http.MethodPost}},
		{name: "blog-post-detail", pattern: "posts/<str:slug>", view: retrievePost, methods: []string{http.MethodGet}},
		{name: "blog-comment-create", pattern: "posts/<str:slug>/comments", view: createComment, methods: []string{http.MethodPost}},
		{name: "blog-tag-list", pattern: "tags", view: listTags, methods: []string{http.MethodGet}},
	}
	for _, route := range routes {
		if err := router.Handle(route.name, route.pattern, route.view, route.methods...); err != nil {
			return err
		}
	}
	return nil
}

func listPosts(context.Context, *api.Request) api.Response {
	return api.JSON(http.StatusOK, map[string]any{
		"count":   0,
		"results": []map[string]any{},
	})
}

func createPost(context.Context, *api.Request) api.Response {
	return api.Created(map[string]any{"status": "accepted"})
}

func retrievePost(_ context.Context, request *api.Request) api.Response {
	return api.JSON(http.StatusOK, map[string]any{"slug": request.PathParam("slug")})
}

func createComment(context.Context, *api.Request) api.Response {
	return api.Created(map[string]any{"status": "pending"})
}

func listTags(context.Context, *api.Request) api.Response {
	return api.JSON(http.StatusOK, map[string]any{"results": []map[string]any{}})
}
