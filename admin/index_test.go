package admin

import (
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/models"
)

func TestAdminIndexFiltersAppsAndModelsByPermissions(t *testing.T) {
	site := DefaultSite()
	registerIndexModel(t, site, "blog", "Post")
	registerIndexModel(t, site, "shop", "Order")
	router, err := site.URLs()
	if err != nil {
		t.Fatalf("URLs() error = %v", err)
	}
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{
		ID:       1,
		IsActive: true,
		UserPermissions: []auth.Permission{
			{Codename: "view_post", ContentType: auth.ContentType{AppLabel: "blog", Model: "post"}},
			{Codename: "add_post", ContentType: auth.ContentType{AppLabel: "blog", Model: "post"}},
		},
	}}}

	index, err := BuildIndex(site, router, user)
	if err != nil {
		t.Fatalf("BuildIndex() error = %v", err)
	}
	if len(index.Apps) != 1 || index.Apps[0].AppLabel != "blog" || len(index.Apps[0].Models) != 1 {
		t.Fatalf("index = %#v", index)
	}
	post := index.Apps[0].Models[0]
	if post.Name != "Post" || post.ChangeURL != "/admin/blog/post/" || post.AddURL != "/admin/blog/post/add/" {
		t.Fatalf("post entry = %#v", post)
	}

	appList, err := BuildAppList(site, router, user, "blog")
	if err != nil {
		t.Fatalf("BuildAppList() error = %v", err)
	}
	if !reflect.DeepEqual(appList.Apps, index.Apps) {
		t.Fatalf("app list = %#v, want %#v", appList.Apps, index.Apps)
	}
}

func TestAdminIndexSuperuserSeesEveryModel(t *testing.T) {
	site := DefaultSite()
	registerIndexModel(t, site, "blog", "Post")
	registerIndexModel(t, site, "shop", "Order")
	router, _ := site.URLs()
	user := auth.User{AbstractUser: auth.AbstractUser{AbstractBaseUser: auth.AbstractBaseUser{ID: 1, IsActive: true, IsSuperuser: true}}}

	index, err := BuildIndex(site, router, user)
	if err != nil {
		t.Fatalf("BuildIndex() error = %v", err)
	}
	if got := []string{index.Apps[0].AppLabel, index.Apps[1].AppLabel}; !reflect.DeepEqual(got, []string{"blog", "shop"}) {
		t.Fatalf("apps = %#v", index.Apps)
	}
}

func registerIndexModel(t *testing.T, site *Site, appLabel, modelName string) {
	t.Helper()
	if err := site.ModelRegistry.RegisterMetadata(models.Metadata{AppLabel: appLabel, ModelName: modelName, TableName: appLabel + "_" + modelName}, ModelAdmin{}); err != nil {
		t.Fatalf("RegisterMetadata(%s.%s) error = %v", appLabel, modelName, err)
	}
}
