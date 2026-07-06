package orm

import (
	"testing"

	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm/dialects/postgres"
)

type managerTestModel struct{ models.BaseModel }

func (managerTestModel) ModelMeta() models.Metadata {
	return testCompilerModel()
}

func TestManagersUseModelMetadataNames(t *testing.T) {
	meta := testCompilerModel()
	meta.DefaultManagerName = "published"
	meta.BaseManagerName = "all_objects"
	managers := ManagersForModel(meta, NewCompiler(postgres.New()))

	if managers.Default.Name != "published" || managers.Base.Name != "all_objects" {
		t.Fatalf("manager names = %#v", managers)
	}
	if managers.Default.QuerySet().Query().Model.ModelName != "Post" {
		t.Fatalf("manager queryset model = %#v", managers.Default.QuerySet().Query().Model)
	}
}

func TestCustomManagerMethods(t *testing.T) {
	manager := NewManager(testCompilerModel(), NewCompiler(postgres.New())).
		WithMethod("published", func(qs QuerySet) QuerySet {
			return qs.Filter(Predicate{Field: "active", Lookup: LookupExact, Value: true})
		})

	qs, ok := manager.Call("published")
	if !ok {
		t.Fatalf("custom manager method missing")
	}
	if len(qs.Query().Filters) != 1 || qs.Query().Filters[0].Field != "active" {
		t.Fatalf("custom queryset = %#v", qs.Query())
	}
}

func TestTypedManagerAndInheritance(t *testing.T) {
	typed := NewTypedManager[managerTestModel](testCompilerModel(), NewCompiler(postgres.New()))
	if typed.ModelName() != "Post" {
		t.Fatalf("ModelName() = %q", typed.ModelName())
	}

	child := testCompilerModel()
	child.ModelName = "FeaturedPost"
	inherited := typed.Manager.Inherit(child)
	if inherited.Meta.ModelName != "FeaturedPost" || inherited.Name != typed.Name {
		t.Fatalf("inherited manager = %#v", inherited)
	}
}
