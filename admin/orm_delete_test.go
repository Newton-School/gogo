package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestAutomaticJoinRequiresAuthorizedGraphEndpoint(t *testing.T) {
	site, database := newTestSite(t)
	scope := &testScope{db: database, tenant: principal().ID}
	root := scope.object(database.records["1"])
	hidden := scope.object(database.records["2"])
	graph := Deletion{Objects: []Object{root}, JoinRemovals: []JoinRemoval{{Endpoint: hidden}}}
	if _, err := site.scopedDeletion(context.Background(), principal(), graph, true); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("accepted unrelated join endpoint: %v", err)
	}
	graph.JoinRemovals[0].Endpoint = root
	result, err := site.scopedDeletion(context.Background(), principal(), graph, true)
	if err != nil || len(result.JoinRemovals) != 1 || len(result.Objects) != 1 {
		t.Fatalf("authorized join removal: %+v %v", result, err)
	}
}

func TestORMJoinRemovalChecksFrameworkProvenanceAndExactFK(t *testing.T) {
	target := models.Schema{AppLabel: "shop", Name: "Tag", Fields: []models.Field{models.BigAutoField("id")}}
	field := models.ManyToManyField("tags", models.Relation{Target: target.Key()})
	source := models.Schema{AppLabel: "shop", Name: "Post", Fields: []models.Field{models.BigAutoField("id"), field}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{source, target} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	through, err := models.ImplicitThrough(source, field, target)
	if err != nil {
		t.Fatal(err)
	}
	through, _ = registry.Get(through.Key())
	endpoint, _ := models.NewRecord(source)
	_ = endpoint.Set("id", int64(1))
	join, _ := models.NewRecord(through)
	_ = join.Set("id", int64(8))
	_ = join.Set("source_id", int64(1))
	_ = join.Set("target_id", int64(100)) // Never authorized or exposed as a model.
	scope := &ormScoped{owner: &ORMStore{config: ORMConfig{Store: &orm.Store{Registry: registry}}}}
	plan := orm.DeletionPlan{Objects: []models.Record{endpoint}, JoinRemovals: []orm.JoinRemoval{{Record: join, Endpoint: endpoint, Field: "source_id"}}}
	graph, err := scope.deletionFromPlan(plan)
	if err != nil || len(graph.Objects) != 1 || len(graph.JoinRemovals) != 1 {
		t.Fatalf("graph: %+v %v", graph, err)
	}
	plan.Objects = nil
	if _, err := scope.deletionFromPlan(plan); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("accepted endpoint outside fresh graph: %v", err)
	}
	plan.Objects = []models.Record{endpoint}
	_ = join.Set("source_id", int64(2))
	if _, err := scope.deletionFromPlan(plan); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatalf("accepted wrong endpoint FK: %v", err)
	}
	plan.JoinRemovals[0].Record = endpoint
	if _, err := scope.deletionFromPlan(plan); err == nil {
		t.Fatal("ordinary model accepted as automatic intermediary")
	}
}
