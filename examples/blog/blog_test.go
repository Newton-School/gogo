package blog

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/api"
	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/queue"
	gogotest "github.com/Newton-School/gogo/testing"
)

func TestBlogExampleExercisesFrameworkFeatures(t *testing.T) {
	metadata := metadataByLabel(ModelMetadata())
	for _, label := range []string{"blog.Author", "blog.Post", "blog.Tag", "blog.Comment", "blog.AuditEvent"} {
		if _, ok := metadata[label]; !ok {
			t.Fatalf("model metadata %s was not registered", label)
		}
	}
	post := metadata["blog.Post"]
	if post.TableName != "blog_post" || !hasField(post.Fields, "author", "blog.Author") {
		t.Fatalf("post metadata did not include expected table and author relation: %#v", post)
	}
	if len(post.Indexes) == 0 || len(post.Constraints) == 0 {
		t.Fatalf("post metadata must include production indexes and constraints")
	}

	planned := Migrations()
	if len(planned) != 1 {
		t.Fatalf("expected one initial blog migration, got %d", len(planned))
	}
	initial := planned[0]
	if initial.AppLabel != "blog" || initial.Name != migrations.InitialMigrationName() || len(initial.Operations) == 0 {
		t.Fatalf("initial migration is incomplete: %#v", initial)
	}
	if err := initial.Validate(); err != nil {
		t.Fatalf("initial migration should validate: %v", err)
	}

	registry := admin.NewRegistry()
	if err := RegisterAdmin(registry); err != nil {
		t.Fatalf("RegisterAdmin returned error: %v", err)
	}
	for _, label := range []string{"blog.Author", "blog.Post", "blog.Comment", "blog.AuditEvent"} {
		if !registry.IsRegistered(label) {
			t.Fatalf("admin registry missing %s", label)
		}
	}
	postAdmin, ok := registry.GetAdmin("blog.Post")
	if !ok || !contains(postAdmin.ListDisplay, "title") || len(postAdmin.PrepopulatedFields["slug"]) == 0 {
		t.Fatalf("post admin missing list display or slug prepopulation: %#v", postAdmin)
	}

	router := api.NewRouter(api.WithAPIPrefix("api"))
	if err := RegisterAPI(router); err != nil {
		t.Fatalf("RegisterAPI returned error: %v", err)
	}
	postListURL, err := router.Reverse("blog-post-list", nil)
	if err != nil || postListURL != "/api/posts/" {
		t.Fatalf("post list URL = %q, err=%v", postListURL, err)
	}
	_, errors, ok := PostSerializer().Validate(map[string]any{
		"title":     "Launching Gogo",
		"slug":      "launching-gogo",
		"body":      "A practical introduction to the Gogo framework.",
		"status":    "published",
		"author_id": 1,
	})
	if !ok {
		t.Fatalf("post serializer rejected valid data: %#v", errors)
	}

	commentForm := NewCommentForm(map[string]any{
		"name":    "Reader",
		"email":   "reader@example.com",
		"body":    "This is useful.",
		"consent": true,
	})
	if !commentForm.IsValid() {
		t.Fatalf("comment form rejected valid data: %#v", commentForm.Errors())
	}

	if !strings.Contains(Templates()["blog/post_detail.html"], "{{ .Post.Title }}") {
		t.Fatalf("post detail template is missing post title binding")
	}
	if !strings.Contains(StaticFiles()["blog/app.css"], ".blog-post") {
		t.Fatalf("blog stylesheet is missing expected selector")
	}
	if _, err := os.Stat("templates/blog/post_detail.html"); err != nil {
		t.Fatalf("template file is missing from example: %v", err)
	}
	if _, err := os.Stat("static/blog/app.css"); err != nil {
		t.Fatalf("static file is missing from example: %v", err)
	}

	staff := StaffUser()
	if !auth.HasPerm(staff, "blog.change_post") || !auth.HasModulePerms(staff, "blog") {
		t.Fatalf("staff user does not have expected blog permissions: %#v", auth.GetAllPermissions(staff))
	}

	outbox := gogotest.NewMailOutbox()
	queueApp := queue.NewApp(queue.AppOptions{})
	if err := RegisterTasks(queueApp, outbox.Backend()); err != nil {
		t.Fatalf("RegisterTasks returned error: %v", err)
	}
	harness := gogotest.NewQueueHarness(queueApp)
	result, err := harness.Apply(context.Background(), queue.NewSignature("blog.send_comment_notification", "reader@example.com", "Launching Gogo"))
	if err != nil || result.State != queue.StateSuccess {
		t.Fatalf("notification task result = %#v, err=%v", result, err)
	}
	outbox.AssertEmailSent(t, "New blog comment")

	result, err = harness.Apply(context.Background(), queue.NewSignature("blog.audit_event", "publish", "post:1"))
	if err != nil || result.State != queue.StateSuccess || result.Result != "audit:publish:post:1" {
		t.Fatalf("audit task result = %#v, err=%v", result, err)
	}
}

func metadataByLabel(values []ModelMeta) map[string]ModelMeta {
	byLabel := make(map[string]ModelMeta, len(values))
	for _, meta := range values {
		byLabel[meta.Label()] = meta
	}
	return byLabel
}

func hasField(fields []FieldMeta, name string, relation string) bool {
	for _, field := range fields {
		if field.Name == name && field.RelationTarget == relation {
			return true
		}
	}
	return false
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
