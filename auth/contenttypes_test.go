package auth

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/models"
)

func TestContentTypesGenerateFromModelRegistryAndLookup(t *testing.T) {
	modelRegistry := models.NewRegistry()
	if err := modelRegistry.RegisterMetadata(models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post"}); err != nil {
		t.Fatalf("RegisterMetadata(Post) error = %v", err)
	}
	if err := modelRegistry.RegisterMetadata(models.Metadata{AppLabel: "shop", ModelName: "Order", TableName: "shop_order"}); err != nil {
		t.Fatalf("RegisterMetadata(Order) error = %v", err)
	}

	contentTypes, err := NewContentTypeRegistryFromModels(modelRegistry)
	if err != nil {
		t.Fatalf("NewContentTypeRegistryFromModels() error = %v", err)
	}

	post, ok := contentTypes.LookupByModel("blog", "Post")
	if !ok {
		t.Fatalf("LookupByModel(blog, Post) missing")
	}
	if post.ID != 1 || post.AppLabel != "blog" || post.Model != "post" {
		t.Fatalf("post content type = %#v", post)
	}
	if post.NaturalKey() != "blog.post" {
		t.Fatalf("NaturalKey() = %q", post.NaturalKey())
	}

	byNaturalKey, ok := contentTypes.LookupNaturalKey("shop", "order")
	if !ok || byNaturalKey.ID != 2 {
		t.Fatalf("LookupNaturalKey(shop, order) = %#v, %v", byNaturalKey, ok)
	}

	byID, ok := contentTypes.LookupID(1)
	if !ok || byID.NaturalKey() != "blog.post" {
		t.Fatalf("LookupID(1) = %#v, %v", byID, ok)
	}
}

func TestContentTypeRegistryRejectsDuplicatesAndFindsStaleRows(t *testing.T) {
	registry := NewContentTypeRegistry()
	if _, err := registry.Register(ContentType{AppLabel: "blog", Model: "post"}); err != nil {
		t.Fatalf("Register(post) error = %v", err)
	}
	if _, err := registry.Register(ContentType{AppLabel: "blog", Model: "Post"}); !errors.Is(err, ErrDuplicateContentType) {
		t.Fatalf("Register(duplicate) error = %v, want ErrDuplicateContentType", err)
	}

	stale := registry.StaleContentTypes([]ContentType{
		{ID: 1, AppLabel: "blog", Model: "post"},
		{ID: 99, AppLabel: "old", Model: "entry"},
	})
	if !reflect.DeepEqual(stale, []ContentType{{ID: 99, AppLabel: "old", Model: "entry"}}) {
		t.Fatalf("stale = %#v", stale)
	}
}
