package orm

import (
	"testing"

	"github.com/Newton-School/gogo/models"
)

func TestDefaultRouterRoutesToDefaultDatabase(t *testing.T) {
	router := DefaultRouter{}
	meta := models.Metadata{AppLabel: "blog", ModelName: "Post"}

	if db := router.DBForRead(meta); db != DefaultDatabase {
		t.Fatalf("DBForRead() = %q", db)
	}
	if db := router.DBForWrite(meta); db != DefaultDatabase {
		t.Fatalf("DBForWrite() = %q", db)
	}
	if !router.AllowRelation(meta, models.Metadata{AppLabel: "auth", ModelName: "User"}) {
		t.Fatalf("AllowRelation() = false, want true")
	}
	if !router.AllowMigrate(DefaultDatabase, "blog", "Post") {
		t.Fatalf("AllowMigrate() = false, want true")
	}
}

func TestRouterSetUsesFirstCustomOpinionAndDefaultFallback(t *testing.T) {
	router := NewRouterSet(FuncRouter{
		Read: func(meta models.Metadata) (string, bool) {
			if meta.AppLabel == "analytics" {
				return "replica", true
			}
			return "", false
		},
		Write: func(meta models.Metadata) (string, bool) {
			if meta.AppLabel == "analytics" {
				return "warehouse", true
			}
			return "", false
		},
	})

	analytics := models.Metadata{AppLabel: "analytics", ModelName: "Event"}
	blog := models.Metadata{AppLabel: "blog", ModelName: "Post"}
	if db := router.DBForRead(analytics); db != "replica" {
		t.Fatalf("analytics read database = %q", db)
	}
	if db := router.DBForWrite(analytics); db != "warehouse" {
		t.Fatalf("analytics write database = %q", db)
	}
	if db := router.DBForRead(blog); db != DefaultDatabase {
		t.Fatalf("fallback read database = %q", db)
	}
}

func TestRouterSetAllowsMigrationDecisions(t *testing.T) {
	router := NewRouterSet(FuncRouter{
		Migrate: func(db, appLabel, modelName string) (bool, bool) {
			if db == "replica" {
				return false, true
			}
			if appLabel == "audit" && modelName == "Event" {
				return true, true
			}
			return false, false
		},
	})

	if router.AllowMigrate("replica", "blog", "Post") {
		t.Fatalf("replica migration was allowed")
	}
	if !router.AllowMigrate("warehouse", "audit", "Event") {
		t.Fatalf("audit event migration was denied")
	}
	if !router.AllowMigrate(DefaultDatabase, "blog", "Post") {
		t.Fatalf("fallback migration should be allowed")
	}
}

func TestRouterSetAllowsRelationDecisions(t *testing.T) {
	router := NewRouterSet(FuncRouter{
		Relation: func(a, b models.Metadata) (bool, bool) {
			if a.AppLabel == "tenant_a" && b.AppLabel == "tenant_b" {
				return false, true
			}
			return false, false
		},
	})

	if router.AllowRelation(models.Metadata{AppLabel: "tenant_a"}, models.Metadata{AppLabel: "tenant_b"}) {
		t.Fatalf("cross-tenant relation was allowed")
	}
	if !router.AllowRelation(models.Metadata{AppLabel: "blog"}, models.Metadata{AppLabel: "auth"}) {
		t.Fatalf("fallback relation should be allowed")
	}
}
