package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// The fixture supplies exactly the selected columns. A value on an unselected
// source field cannot accidentally become available through the fake reader.
type genericModelRowsReader struct {
	names                   []string
	data                    []map[string]any
	position, nexts, closes int
	queryErr, scanErr       error
	terminalErr, closeErr   error
	nextHook, closeHook     func()
}

func (r *genericModelRowsReader) Columns() ([]string, error) {
	return append([]string(nil), r.names...), nil
}
func (r *genericModelRowsReader) Next() bool {
	r.nexts++
	if r.nextHook != nil {
		r.nextHook()
	}
	if r.position == len(r.data) {
		return false
	}
	r.position++
	return true
}
func (r *genericModelRowsReader) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	if len(dest) != len(r.names) || r.position < 1 || r.position > len(r.data) {
		return errors.New("invalid row fixture projection")
	}
	for i, name := range r.names {
		*dest[i].(*any) = r.data[r.position-1][name]
	}
	return nil
}
func (r *genericModelRowsReader) Err() error {
	if r.position == len(r.data) {
		return r.terminalErr
	}
	return nil
}
func (r *genericModelRowsReader) Close() error {
	r.closes++
	if r.closeHook != nil {
		r.closeHook()
	}
	return r.closeErr
}

type genericModelRowsBackend struct {
	*genericModelTestBackend
	rows *genericModelRowsReader
}

func (b *genericModelRowsBackend) Query(_ context.Context, sql string, args ...any) (db.Rows, error) {
	b.queries++
	if b.query != nil {
		b.query(sql, args)
	}
	return b.rows, b.rows.queryErr
}

func genericModelRowsFixture(t *testing.T, data []map[string]any, change func(*ModelReadOptions)) (*genericModel, *genericModelRowsReader, orm.Query[*models.MapRecord]) {
	t.Helper()
	options, backend := genericModelTestOptions(t)
	if change != nil {
		change(&options)
	}
	reader := &genericModelRowsReader{data: data}
	options.Store = orm.New(&genericModelRowsBackend{genericModelTestBackend: backend, rows: reader}, options.Store.Registry)
	model, err := newGenericModel(options)
	if err != nil {
		t.Fatal(err)
	}
	reader.names = append([]string(nil), model.selected...)
	query, err := model.query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return model, reader, query
}

func TestGenericModelReadRowsMaterializesBeforeProviderReuse(t *testing.T) {
	buffer := []byte("first")
	model, reader, query := genericModelRowsFixture(t, []map[string]any{
		{"id": int64(1), "title": buffer, "secret": genericModelOpaque{}},
		{"id": int64(2), "title": buffer, "secret": genericModelOpaque{}},
	}, nil)
	reader.nextHook = func() {
		if reader.nexts == 2 {
			copy(buffer, "later")
		}
	}
	reader.closeHook = func() { copy(buffer, "close") }
	rows, err := model.readRows(context.Background(), query, 2)
	if err != nil || len(rows) != 2 || reader.closes != 1 || reader.nexts != 3 {
		t.Fatal("complete stream failed", len(rows), err, reader.closes, reader.nexts)
	}
	if rows[0].values["title"] != "first" || rows[1].values["title"] != "later" {
		t.Fatal("driver reused storage changed an earlier row", rows)
	}
	for _, row := range rows {
		if len(row.values) != 2 || row.database != "generic_model_test" {
			t.Fatal("unselected data or wrong database entered snapshot", row)
		}
	}
}

func TestGenericModelReadRowsNeverReturnsFailedPrefix(t *testing.T) {
	for _, mode := range []string{"empty", "query", "scan", "terminal", "close", "late cancellation", "overflow", "malformed second row"} {
		t.Run(mode, func(t *testing.T) {
			data := []map[string]any{{"id": int64(1), "title": "first"}, {"id": int64(2), "title": "second"}}
			if mode == "empty" {
				data = nil
			}
			model, reader, query := genericModelRowsFixture(t, data, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("private provider completion failure")
			limit := 2
			switch mode {
			case "query":
				reader.queryErr = failure
			case "scan":
				reader.scanErr = failure
			case "terminal":
				reader.terminalErr = failure
			case "close":
				reader.closeErr = failure
			case "late cancellation":
				reader.closeHook = cancel
			case "overflow":
				limit = 1
			case "malformed second row":
				data[1]["title"] = genericModelOpaque{}
			}
			rows, err := model.readRows(ctx, query, limit)
			if mode == "empty" {
				if err != nil || len(rows) != 0 {
					t.Fatal("valid empty result failed", rows, err)
				}
			} else if rows != nil || err != ErrUnavailable {
				t.Fatal("partial provider result escaped", rows, err)
			}
			if reader.closes != 1 {
				t.Fatal("reader not closed exactly once", reader.closes)
			}
		})
	}
	for _, limit := range []int{-1, 0, 202} {
		model, reader, query := genericModelRowsFixture(t, nil, nil)
		if rows, err := model.readRows(context.Background(), query, limit); rows != nil || err != ErrUnavailable || reader.nexts != 0 || reader.closes != 0 {
			t.Fatal("invalid cap started a stream", limit, rows, err)
		}
	}
	model, reader, query := genericModelRowsFixture(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if rows, err := model.readRows(ctx, query, 1); rows != nil || err != ErrUnavailable || reader.nexts != 0 {
			t.Fatal("invalid context started a stream", rows, err)
		}
	}
}

func TestGenericModelReadRowsUsesPageWidePolicyDataBudget(t *testing.T) {
	for _, mode := range []string{"bytes", "nodes", "raw JSON work"} {
		t.Run(mode, func(t *testing.T) {
			var raw any = map[string]any{"text": strings.Repeat("x", templateContextMaxBytes/2)}
			if mode == "nodes" {
				raw = make([]any, templateContextMaxValues/2)
			} else if mode == "raw JSON work" {
				// A provider may supply RawMessage directly; ORM preserves that
				// explicit type, while byte/string JSON uses its native decoder.
				// Both cells decode to tiny nulls but share local parsing work.
				raw = json.RawMessage(strings.Repeat(" ", templateContextMaxBytes/2) + "null")
			}
			data := []map[string]any{
				{"id": int64(1), "title": "one", "policy": raw},
				{"id": int64(2), "title": "two", "policy": raw},
			}
			for _, count := range []int{1, 2} {
				model, reader, query := genericModelRowsFixture(t, data[:count], func(options *ModelReadOptions) {
					schema := genericModelTestSchema()
					schema.Fields = append(schema.Fields, models.JSONField("policy"))
					registry := &models.Registry{}
					if err := registry.Register(schema); err != nil {
						t.Fatal(err)
					}
					options.Store.Registry = registry
					options.PolicyFields = []string{"policy"}
				})
				rows, err := model.readRows(context.Background(), query, 2)
				if count == 1 {
					if len(rows) != 1 || err != nil {
						t.Fatal("first cell failed independently, so it cannot prove page accounting", len(rows), err)
					}
				} else if rows != nil || err != ErrUnavailable {
					t.Fatal("individually valid policy values multiplied the page budget", len(rows), err)
				}
				if reader.closes != 1 {
					t.Fatal("page budget path did not close the stream", reader.closes)
				}
			}
		})
	}
}

func TestGenericModelReadRowsAcceptsExactMaximumCount(t *testing.T) {
	data := make([]map[string]any, 201)
	for i := range data {
		data[i] = map[string]any{"id": int64(i + 1), "title": "value"}
	}
	model, reader, query := genericModelRowsFixture(t, data, nil)
	rows, err := model.readRows(context.Background(), query, 201)
	if err != nil || len(rows) != 201 || reader.nexts != 202 || reader.closes != 1 {
		t.Fatal("exact bounded result refused", len(rows), err, reader.nexts, reader.closes)
	}
}

func TestGenericModelReadOptionsRetainsOriginalIdentityContext(t *testing.T) {
	options, _ := genericModelTestOptions(t)
	options.AllowAnonymous = true
	var order []string
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "original")
	options.Policy = auth.PolicyFunc(func(current context.Context, _ auth.Principal, action string, resource auth.Resource) error {
		order = append(order, "model")
		if current.Value(key{}) != "original" || action != "view" || resource.ID != nil || resource.Object != nil {
			t.Fatal("application hook retargeted model authorization", current.Value(key{}), resource)
		}
		return nil
	})
	model, err := newGenericModel(options)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := model.readOptions(ReadViewOptions{AllowOptions: true, Timeout: time.Second, Authorize: func(request *http.Request) error {
		order = append(order, "application")
		*request = *request.WithContext(context.WithValue(context.Background(), key{}, "replacement"))
		return nil
	}})
	if err := wrapped.Authorize(httptest.NewRequest("GET", "/", nil).WithContext(ctx)); err != nil || !reflect.DeepEqual(order, []string{"application", "model"}) || !wrapped.AllowOptions || wrapped.Timeout != time.Second {
		t.Fatal("composed access contract changed", err, order, wrapped)
	}
	order = nil
	denied := model.readOptions(ReadViewOptions{Authorize: func(*http.Request) error { return auth.ErrPermissionDenied }})
	if err := denied.Authorize(httptest.NewRequest("GET", "/", nil).WithContext(ctx)); err != auth.ErrPermissionDenied || len(order) != 0 {
		t.Fatal("application denial still invoked model policy", err, order)
	}
	if empty := model.readOptions(ReadViewOptions{}); empty.Authorize != nil {
		t.Fatal("missing mandatory authorization received a permissive default")
	}
}

func TestGenericModelReadRowsRejectsNullableDeclaredIdentity(t *testing.T) {
	model, reader, query := genericModelRowsFixture(t, []map[string]any{{"id": nil, "title": "value"}}, func(options *ModelReadOptions) {
		schema := genericModelTestSchema()
		schema.Fields[0] = models.BigIntegerField("id", models.Nullable)
		schema.PrimaryKey = []string{"id"}
		registry := &models.Registry{}
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
		options.Store.Registry = registry
	})
	if rows, err := model.readRows(context.Background(), query, 1); rows != nil || err != ErrUnavailable || reader.closes != 1 {
		t.Fatal("schema-level primary key accepted NULL", rows, err, reader.closes)
	}
}

func genericModelProjectionFixture(t *testing.T, policy auth.Policy, allow func(context.Context, auth.Principal, models.Record, string) (bool, error)) (*genericModel, []genericModelRow) {
	t.Helper()
	schema := genericModelTestSchema()
	schema.Fields = append(schema.Fields, models.JSONField("payload"), models.DateTimeField("published"))
	options, _ := genericModelTestOptions(t, schema)
	options.Fields = []string{"title", "payload", "published"}
	options.PolicyFields = []string{"tenant"}
	options.AllowAnonymous = true
	if policy != nil {
		options.Policy = policy
	}
	options.AllowField = allow
	model, err := newGenericModel(options)
	if err != nil {
		t.Fatal(err)
	}
	return model, []genericModelRow{{database: "original", values: map[string]any{
		"id": int64(7), "tenant": int64(42), "title": "original",
		"payload":   map[string]any{"n": json.Number("9007199254740993"), "nested": []any{"original"}},
		"published": time.Date(2024, 1, 2, 3, 4, 5, 6, time.FixedZone("original", 0)),
	}}}
}

func TestGenericModelProjectionUsesFreshPolicyRecords(t *testing.T) {
	var records []models.Record
	check := func(record models.Record) {
		t.Helper()
		for _, previous := range records {
			if previous == record {
				t.Fatal("policy callbacks share a record")
			}
		}
		records = append(records, record)
		for name, want := range map[string]any{"id": int64(7), "tenant": int64(42), "title": "original"} {
			if value, err := record.Get(name); err != nil || value != want {
				t.Fatal("previous callback changed canonical data", name, value, err)
			}
		}
		if value, err := record.Get("secret"); value != nil || err == nil {
			t.Fatal("unselected secret was not deferred", value, err)
		}
		payload, _ := record.Get("payload")
		if payload.(map[string]any)["n"] != json.Number("9007199254740993") || payload.(map[string]any)["nested"].([]any)[0] != "original" {
			t.Fatal("canonical JSON changed between callbacks", payload)
		}
		stamp, _ := record.Get("published")
		if _, offset := stamp.(time.Time).Zone(); offset != 0 {
			t.Fatal("earlier callback changed a timestamp")
		}
		if state := record.State(); !state.Persisted || state.Database != "original" || !state.Deferred["secret"] {
			t.Fatal("earlier callback changed record state", state)
		}
		_ = record.Set("id", int64(99))
		_ = record.Set("title", "mutated")
		payload.(map[string]any)["nested"].([]any)[0] = "mutated"
		*stamp.(time.Time).Location() = *time.FixedZone("mutated", 3600)
		record.State().Database = "mutated"
		record.State().Deferred["secret"] = false
	}
	objectCalls, fieldCalls := 0, 0
	model, rows := genericModelProjectionFixture(t, auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, resource auth.Resource) error {
		objectCalls++
		check(resource.Object.(models.Record))
		id := resource.ID.(map[string]any)
		if id["id"] != int64(7) {
			t.Fatal("object policy got mutable identity", id)
		}
		id["id"] = int64(99)
		return nil
	}), func(_ context.Context, _ auth.Principal, record models.Record, _ string) (bool, error) {
		fieldCalls++
		check(record)
		return true, nil
	})
	output, finalize, err := model.projectRows(context.Background(), rows, false)
	if err != nil || finalize == nil || len(output) != 1 || len(output[0]) != 3 {
		t.Fatal("projection failed", output, err)
	}
	if output[0]["title"] != "original" || output[0]["payload"].(map[string]any)["n"] != "9007199254740993" || output[0]["payload"].(map[string]any)["nested"].([]any)[0] != "original" {
		t.Fatal("policy mutation entered template output", output)
	}
	// Even changing the separate presentation after projection cannot retarget
	// a later policy decision over the private canonical rows.
	output[0]["payload"].(map[string]any)["nested"].([]any)[0] = "presentation mutation"
	if err := finalize(&readViewCall{base: httptest.NewRequest("GET", "/", nil)}); err != nil {
		t.Fatal(err)
	}
	if objectCalls != 2 || fieldCalls != 6 || len(records) != 8 {
		t.Fatal("missing initial/final policy checks", objectCalls, fieldCalls, len(records))
	}
}

func TestGenericModelProjectionOmissionNeverWidensAtFinalCheck(t *testing.T) {
	final := false
	var fields []string
	model, rows := genericModelProjectionFixture(t, nil, func(_ context.Context, _ auth.Principal, _ models.Record, name string) (bool, error) {
		fields = append(fields, fmt.Sprintf("%t:%s", final, name))
		return name != "payload" || final, nil
	})
	output, finalize, err := model.projectRows(context.Background(), rows, false)
	if err != nil || len(output) != 1 || len(output[0]) != 2 {
		t.Fatal(output, err)
	}
	for _, name := range []string{"payload", "id", "tenant", "secret"} {
		if _, ok := output[0][name]; ok {
			t.Fatal("denied or policy-only data exposed", name)
		}
	}
	final = true
	if err := finalize(&readViewCall{base: httptest.NewRequest("GET", "/", nil)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := output[0]["payload"]; ok || !reflect.DeepEqual(fields, []string{"false:title", "false:payload", "false:published", "true:title", "true:published"}) {
		t.Fatal("final phase reopened an omitted field", fields, output)
	}
}

func TestGenericModelProjectionFinalRevocationAndFailures(t *testing.T) {
	for _, detail := range []bool{false, true} {
		for _, mode := range []string{"object deny", "field deny", "object error", "field error", "object cancels", "field cancels"} {
			t.Run(fmt.Sprintf("%t/%s", detail, mode), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				final := false
				failure := errors.New("private policy failure")
				model, rows := genericModelProjectionFixture(t, auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error {
					if final {
						switch mode {
						case "object deny":
							return auth.ErrPermissionDenied
						case "object error":
							return failure
						case "object cancels":
							cancel()
							return auth.ErrPermissionDenied
						}
					}
					return nil
				}), func(context.Context, auth.Principal, models.Record, string) (bool, error) {
					if final {
						switch mode {
						case "field deny":
							return false, nil
						case "field error":
							return false, failure
						case "field cancels":
							cancel()
							return false, nil
						}
					}
					return true, nil
				})
				_, finalize, err := model.projectRows(ctx, rows, detail)
				if err != nil {
					t.Fatal(err)
				}
				final = true
				err = finalize(&readViewCall{base: httptest.NewRequest("GET", "/", nil).WithContext(ctx)})
				want := error(ErrUnavailable)
				if mode == "object deny" || mode == "field deny" {
					want = auth.ErrPermissionDenied
					if detail {
						want = ErrNotFound
					}
				} else if mode == "object error" {
					want = failure // The common boundary maps provider errors safely.
				}
				if err != want {
					t.Fatal("incorrect final result", err, want)
				}
			})
		}
	}
}

func TestGenericModelProjectionDiscardsPrefixOnLaterDenial(t *testing.T) {
	for _, detail := range []bool{false, true} {
		calls := 0
		model, rows := genericModelProjectionFixture(t, auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error {
			calls++
			if calls == 2 {
				return auth.ErrPermissionDenied
			}
			return nil
		}), nil)
		rows = append(rows, rows[0])
		output, final, err := model.projectRows(context.Background(), rows, detail)
		want := error(auth.ErrPermissionDenied)
		if detail {
			want = ErrNotFound
		}
		if output != nil || final != nil || err != want {
			t.Fatal("partial authorized prefix escaped", output, err)
		}
	}
}
