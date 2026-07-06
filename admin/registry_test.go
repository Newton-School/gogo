package admin

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/app"
	"github.com/Newton-School/gogo/models"
)

func TestAdminRegistryRegistersAndUnregistersModels(t *testing.T) {
	registry := NewRegistry()
	meta := models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post"}
	admin := ModelAdmin{Handler: "PostAdmin", ListDisplay: []string{"title"}}

	if err := registry.RegisterMetadata(meta, admin); err != nil {
		t.Fatalf("RegisterMetadata() error = %v", err)
	}
	if !registry.IsRegistered("blog.Post") {
		t.Fatalf("blog.Post should be registered")
	}
	got, ok := registry.GetAdmin("blog.Post")
	if !ok || got.Handler != "PostAdmin" || got.Model.TableName != "blog_post" {
		t.Fatalf("GetAdmin() = %#v, %v", got, ok)
	}
	got.ListDisplay[0] = "changed"
	again, _ := registry.GetAdmin("blog.Post")
	if again.ListDisplay[0] != "title" {
		t.Fatalf("GetAdmin leaked mutable config: %#v", again)
	}
	if err := registry.RegisterMetadata(meta, admin); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("duplicate error = %v, want ErrAlreadyRegistered", err)
	}
	if err := registry.Unregister("blog.Post"); err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}
	if registry.IsRegistered("blog.Post") {
		t.Fatalf("blog.Post should be unregistered")
	}
	if err := registry.Unregister("blog.Post"); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("second Unregister() error = %v, want ErrNotRegistered", err)
	}
}

func TestAdminRegistryRejectsUnmanagedUnlessAllowed(t *testing.T) {
	managed := false
	meta := models.Metadata{AppLabel: "legacy", ModelName: "Report", TableName: "legacy_report", Managed: &managed}
	registry := NewRegistry()

	if err := registry.RegisterMetadata(meta, ModelAdmin{}); !errors.Is(err, ErrUnmanagedModel) {
		t.Fatalf("RegisterMetadata(unmanaged) error = %v, want ErrUnmanagedModel", err)
	}
	if err := registry.RegisterMetadata(meta, ModelAdmin{AllowUnmanaged: true}); err != nil {
		t.Fatalf("RegisterMetadata(allowed unmanaged) error = %v", err)
	}
}

func TestAdminRegistryAutodiscoveryPreservesAppOrder(t *testing.T) {
	apps := app.NewRegistry()
	apps.RegisterAdmin(app.AdminResource{AppLabel: "blog", ModelName: "Post", Handler: "PostAdmin"})
	apps.RegisterAdmin(app.AdminResource{AppLabel: "shop", ModelName: "Order", Handler: "OrderAdmin"})

	registry := NewRegistry()
	if err := registry.Autodiscover(apps); err != nil {
		t.Fatalf("Autodiscover() error = %v", err)
	}
	if got := registry.RegisteredModels(); !reflect.DeepEqual(got, []string{"blog.Post", "shop.Order"}) {
		t.Fatalf("RegisteredModels() = %#v", got)
	}
	post, _ := registry.GetAdmin("blog.Post")
	order, _ := registry.GetAdmin("shop.Order")
	if post.Handler != "PostAdmin" || order.Handler != "OrderAdmin" {
		t.Fatalf("autodiscovered admins = %#v / %#v", post, order)
	}
}
