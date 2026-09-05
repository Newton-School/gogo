package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type savePrepareCodec struct{ encoded []string }

func (c *savePrepareCodec) Encode(value any) (any, error) {
	text := value.(string)
	c.encoded = append(c.encoded, text)
	return "encoded:" + text, nil
}
func (*savePrepareCodec) Decode(value any) (any, error) {
	return strings.TrimPrefix(value.(string), "encoded:"), nil
}

type savePrepareRow struct {
	models.Base
	ID                   int64
	Name, Derived, Label string
	Created, Updated     time.Time
	CleanCalls           int
	codec                *savePrepareCodec
}

func (r *savePrepareRow) Schema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "SavePrepare", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.TextField("name", models.WithStructField("Name"), func(f *models.Field) { f.Codec = r.codec }),
		models.TextField("derived", models.WithStructField("Derived"), models.Optional),
		models.TextField("label", models.WithStructField("Label"), models.WithDefault("default label")),
		models.DateTimeField("created", models.WithStructField("Created"), func(f *models.Field) { f.AutoNowAdd = true }),
		models.DateTimeField("updated", models.WithStructField("Updated"), func(f *models.Field) { f.AutoNow = true }),
	}}
}
func (r *savePrepareRow) Clean(context.Context) error {
	r.CleanCalls++
	r.Name = strings.TrimSpace(r.Name)
	r.Derived = r.Name + " normalized"
	return nil
}

func TestSavePrepareNormalizesBeforeSingleEncodingAndReadonlyGuard(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	codec := &savePrepareCodec{}
	row := &savePrepareRow{codec: codec, Name: "initial"}
	if err := b.SchemaEditor().CreateModel(ctx, b, row.Schema()); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	hooks, preparations, guards := 0, 0, 0
	store.BeforeSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error {
		hooks++
		row.Name = "  after hook  "
		return nil
	}}
	prepare := func(ctx context.Context, record models.Record) error {
		preparations++
		if hooks != 1 || row.Created.IsZero() || row.Updated.IsZero() || row.Label != "default label" || len(codec.encoded) != 0 {
			t.Fatal("mutable preparation ran outside its pre-encoding boundary")
		}
		return models.FullClean(ctx, record, models.CleanOptions{}, store)
	}
	guard := func(context.Context, models.Record) error {
		guards++
		if row.Name != "after hook" || row.Derived != "after hook normalized" || len(codec.encoded) != 1 || codec.encoded[0] != row.Name {
			t.Fatal("guard did not observe normalized encoded values")
		}
		return nil
	}
	if err := store.Save(ctx, row, orm.SaveOptions{ForceInsert: true, Prepare: prepare, Guard: guard}); err != nil {
		t.Fatal(err)
	}
	if row.CleanCalls != 1 || preparations != 1 || guards != 1 {
		t.Fatal("duplicate preparation/validation/guard")
	}
	loaded, err := orm.For(store, func() *savePrepareRow { return &savePrepareRow{codec: codec} }).Filter(orm.Q("id", row.ID)).Get(ctx)
	if err != nil || loaded.Name != "after hook" || loaded.Derived != "after hook normalized" || loaded.Label != "default label" {
		t.Fatal("model Clean normalization was not persisted", err)
	}
	store.BeforeSave = nil
	created, updated := row.Created, row.Updated
	codec.encoded = nil
	row.Name = "  selective  "
	if err := store.Save(ctx, row, orm.SaveOptions{UpdateFields: []string{"name"}, Prepare: func(ctx context.Context, record models.Record) error {
		return models.FullClean(ctx, record, models.CleanOptions{}, store)
	}}); err != nil {
		t.Fatal(err)
	}
	if len(codec.encoded) != 1 || row.Name != "selective" || row.Derived != "after hook normalized" || !row.Created.Equal(created) || !row.Updated.Equal(updated) {
		t.Fatal("Prepare broadened UpdateFields or repeated auto fields")
	}
	// Normal Save still does not perform validation without explicit Prepare.
	row.Name = "  ordinary  "
	cleanCalls := row.CleanCalls
	if err := store.Save(ctx, row, orm.SaveOptions{UpdateFields: []string{"name"}}); err != nil || row.CleanCalls != cleanCalls || row.Name != "  ordinary  " {
		t.Fatal("ordinary Save gained automatic FullClean", err)
	}
}

func TestSavePrepareFallbackRawDenialAndCancellation(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	codec := &savePrepareCodec{}
	row := &savePrepareRow{codec: codec, ID: 91001, Name: "  fallback  "}
	if err := b.SchemaEditor().CreateModel(ctx, b, row.Schema()); err != nil {
		t.Fatal(err)
	}
	counted := &countedBackend{Backend: b}
	store := orm.New(counted, nil)
	preparations, guards := 0, 0
	prepare := func(_ context.Context, record models.Record) error {
		preparations++
		if preparations == 1 && !row.Created.IsZero() || preparations == 2 && row.Created.IsZero() {
			t.Fatal("fallback operation auto-field semantics changed")
		}
		row.Name = strings.TrimSpace(row.Name)
		return record.Set("derived", "fallback normalized")
	}
	if err := store.Save(ctx, row, orm.SaveOptions{Prepare: prepare, Guard: func(context.Context, models.Record) error { guards++; return nil }}); err != nil {
		t.Fatal(err)
	}
	if preparations != 2 || guards != 2 || len(codec.encoded) != 2 || counted.queries.Load() != 2 || row.Name != "fallback" || row.Derived != "fallback normalized" {
		t.Fatal("fallback did not prepare/encode exactly once per attempted statement")
	}
	for _, mode := range []string{"denied", "canceled", "precanceled", "empty", "forced_missing_pk"} {
		t.Run(mode, func(t *testing.T) {
			codec.encoded = nil
			counted.queries.Store(0)
			calls := 0
			candidate := &savePrepareRow{codec: codec, Name: "denied"}
			failure := errors.New("synthetic preparation failure")
			local, cancel := context.WithCancel(ctx)
			defer cancel()
			options := orm.SaveOptions{ForceInsert: true, Prepare: func(context.Context, models.Record) error {
				calls++
				if mode == "canceled" {
					cancel()
					return nil
				}
				return failure
			}}
			switch mode {
			case "precanceled":
				cancel()
			case "empty":
				options.UpdateFields = []string{}
			case "forced_missing_pk":
				options.ForceInsert, options.ForceUpdate = false, true
			}
			err := store.Save(local, candidate, options)
			if mode == "empty" {
				if err != nil || calls != 0 {
					t.Fatal("empty update fields invoked Prepare", err)
				}
			} else if mode == "canceled" || mode == "precanceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("preparation cancellation lost", err)
				}
			} else if err == nil {
				t.Fatal("invalid preparation accepted")
			}
			if counted.queries.Load() != 0 || len(codec.encoded) != 0 || candidate.ModelState().Persisted {
				t.Fatal("failed/skipped preparation encoded or executed SQL")
			}
		})
	}
	codec.encoded = nil
	stamp := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	raw := &savePrepareRow{codec: codec, Name: "raw", Created: stamp, Updated: stamp}
	if err := store.Save(ctx, raw, orm.SaveOptions{ForceInsert: true, Raw: true, Prepare: func(_ context.Context, record models.Record) error {
		if raw.Label != "" || raw.Created != stamp || raw.Updated != stamp {
			t.Fatal("Raw unexpectedly applied defaults/auto fields")
		}
		return record.Set("label", "explicit raw preparation")
	}}); err != nil || raw.Label != "explicit raw preparation" || !raw.Created.Equal(stamp) || !raw.Updated.Equal(stamp) || len(codec.encoded) != 1 {
		t.Fatal("Raw explicit preparation failed", err)
	}
}

func TestSavePrepareUsesFullRecordAtEachParentTableBoundary(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	parent := models.Schema{AppLabel: "tests", Name: "PrepareParent", Fields: []models.Field{models.BigAutoField("id"), models.TextField("name")}}
	child := models.Schema{AppLabel: "tests", Name: "PrepareChild", Parent: parent.Key(), Fields: []models.Field{models.BigAutoField("id"), models.TextField("name"), models.TextField("detail")}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{parent, child} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
		if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(b, registry)
	record, _ := models.NewRecord(child)
	_ = record.Set("name", " parent ")
	_ = record.Set("detail", " child ")
	calls, guards := 0, 0
	if err := store.Save(ctx, record, orm.SaveOptions{ForceInsert: true, Prepare: func(_ context.Context, full models.Record) error {
		calls++
		if full.Schema().Key() != child.Key() {
			t.Fatal("preparation did not receive full child record")
		}
		_ = full.Set("name", strings.TrimSpace(mustValue(t, full, "name").(string)))
		return full.Set("detail", strings.TrimSpace(mustValue(t, full, "detail").(string)))
	}, Guard: func(context.Context, models.Record) error { guards++; return nil }}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || guards != 2 {
		t.Fatal("parent/child attempts did not invoke separate preparation")
	}
	for _, schema := range []models.Schema{parent, child} {
		loaded, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).Get(ctx)
		if err != nil || mustValue(t, loaded, "name") != "parent" {
			t.Fatal("parent/child preparation was not persisted", err)
		}
	}
}
