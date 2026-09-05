package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func mustSerializer(t *testing.T, def Definition) *Serializer {
	t.Helper()
	s, err := New(def)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestFieldDirectionsPresenceAndSecrets(t *testing.T) {
	secret := StringField("password")
	secret.WriteOnly = true
	id := IntegerField("id")
	id.ReadOnly = true
	note := StringField("note", models.Nullable, models.Optional)
	hidden := StringField("owner")
	hidden.Hidden = true
	hidden.Default = func(context.Context) (any, error) { return "trusted-owner", nil }
	s := mustSerializer(t, Definition{Fields: []Field{StringField("name"), id, secret, note, hidden}})
	values, err := s.Validate(context.Background(), Values{"name": "name", "id": "untrusted", "password": "only-input", "owner": "attacker"}, BindOptions{})
	if err != nil || values["owner"] != "trusted-owner" {
		t.Fatal(values, err)
	}
	if _, ok := values["id"]; ok {
		t.Fatal("readonly field became writable")
	}
	partial, err := s.Validate(context.Background(), Values{"note": nil}, BindOptions{Partial: true})
	if err != nil || len(partial) != 1 {
		t.Fatal(partial, err)
	}
	if value, ok := partial["note"]; !ok || value != nil {
		t.Fatal("null collapsed into missing")
	}
	output, err := s.Representation(context.Background(), Values{"name": "name", "id": int64(8), "password": "hash", "owner": "trusted-owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := output["password"]; ok {
		t.Fatal("write-only leaked")
	}
	if _, ok := output["owner"]; ok {
		t.Fatal("hidden leaked")
	}
	if _, err := s.Validate(context.Background(), Values{"private_admin": true}, BindOptions{Partial: true}); err == nil {
		t.Fatal("mass assignment accepted")
	}
}
func TestNestedIndexedErrorsAndExactNumbers(t *testing.T) {
	child := mustSerializer(t, Definition{Fields: []Field{IntegerField("count")}})
	s := mustSerializer(t, Definition{Fields: []Field{ListField("items", NestedField("item", child), 3), DecimalField("amount", 20, 2), JSONField("json")}})
	_, err := s.Validate(context.Background(), Values{"items": []any{Values{"count": "bad"}, Values{"count": nil}}, "amount": "100000000000000001.25", "json": "a JSON string"}, BindOptions{})
	var invalid *ValidationError
	if !errors.As(err, &invalid) || len(invalid.Fields["items.0.count"]) != 1 || len(invalid.Fields["items.1.count"]) != 1 {
		t.Fatal(err, invalid)
	}
	values, err := s.Validate(context.Background(), Values{"items": []any{Values{"count": json.Number("9007199254740993")}}, "amount": "100000000000000001.25", "json": "a JSON string"}, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if values["amount"] != "100000000000000001.25" || values["json"] != "a JSON string" {
		t.Fatal(values)
	}
	if values["items"].([]any)[0].(Values)["count"] != int64(9007199254740993) {
		t.Fatal("integer precision lost")
	}
}
func TestValidationDoesNotWriteAndSaveErrorsDoNotSucceed(t *testing.T) {
	s := mustSerializer(t, Definition{Fields: []Field{IntegerField("count")}})
	writes := 0
	policy := SavePolicy{Atomic: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }, Write: func(_ context.Context, v Values) (ValueReader, error) { writes++; return v, nil }}
	if _, err := s.Save(context.Background(), Values{"count": "bad"}, BindOptions{}, policy); err == nil || writes != 0 {
		t.Fatal("invalid input wrote")
	}
	commitErr := errors.New("uncertain commit")
	policy.Atomic = func(ctx context.Context, fn func(context.Context) error) error {
		if err := fn(ctx); err != nil {
			return err
		}
		return commitErr
	}
	if result, err := s.Save(context.Background(), Values{"count": 1}, BindOptions{}, policy); result != nil || !errors.Is(err, commitErr) {
		t.Fatal(result, err)
	}
}
func TestDescriptorCopiesAndConcurrentValidation(t *testing.T) {
	field := StringField("name", models.WithChoices(models.Choice{Value: "accepted", Label: "Accepted"}))
	s := mustSerializer(t, Definition{Fields: []Field{field}})
	field.Model.Choices[0].Value = "mutated"
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			for range 20 {
				if _, err := s.Validate(context.Background(), Values{"name": "accepted"}, BindOptions{}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
}
func TestInvalidDefinitionsFailEarly(t *testing.T) {
	for _, fields := range [][]Field{{StringField("name"), StringField("name")}, {{Name: "unknown"}}, {{Name: "hidden", Hidden: true, Model: models.CharField("hidden")}}, {{Name: "both", ReadOnly: true, WriteOnly: true, Model: models.CharField("both")}}} {
		if _, err := New(Definition{Fields: fields}); err == nil {
			t.Fatal("invalid descriptor accepted")
		}
	}
}

func TestScalarOutputCannotLeakUndeclaredNestedFields(t *testing.T) {
	s := mustSerializer(t, Definition{Fields: []Field{StringField("name")}})
	if _, err := s.Representation(context.Background(), Values{"name": Values{"private_hash": "fixture-secret"}}); err == nil {
		t.Fatal("scalar serialized undeclared private object")
	}
	for _, direction := range []string{"read", "write", "hidden"} {
		child := StringField("secret")
		child.ReadOnly = direction == "read"
		child.WriteOnly = direction == "write"
		child.Hidden = direction == "hidden"
		if _, err := New(Definition{Fields: []Field{ListField("values", child, 10)}}); err == nil {
			t.Fatal("ambiguous collection child direction accepted", direction)
		}
	}
}

func TestModelSerializerExplicitProjectionAndRelations(t *testing.T) {
	schema := models.Schema{AppLabel: "accounts", Name: "User", Fields: []models.Field{models.BigAutoField("id"), models.CharField("name"), models.CharField("password_hash")}}
	s, err := FromModel(schema, ModelOptions{Fields: []string{"id", "name"}})
	if err != nil {
		t.Fatal(err)
	}
	values, err := s.Validate(context.Background(), Values{"id": 99, "name": "fixture"}, BindOptions{})
	if err != nil || len(values) != 1 {
		t.Fatal(values, err)
	}
	out, err := s.Representation(context.Background(), Values{"id": 7, "name": "fixture", "password_hash": "private"})
	if err != nil || len(out) != 2 {
		t.Fatal(out, err)
	}
	if _, err := FromModel(schema, ModelOptions{}); err == nil {
		t.Fatal("model exposed implicit fields")
	}
	schema.Fields = append(schema.Fields, models.ForeignKeyField("team", models.Relation{Target: "accounts.Team", OnDelete: models.Protect}))
	if _, err := FromModel(schema, ModelOptions{Fields: []string{"team"}}); err == nil {
		t.Fatal("unscoped relation accepted")
	}
}

func TestFileAndImageValidation(t *testing.T) {
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	part, err := writer.CreateFormFile("upload", "fixture.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("not an image"))
	_ = writer.Close()
	r := httptest.NewRequest("POST", "/", &data)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	parsed, err := Parse(r, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer parsed.Close()
	s := mustSerializer(t, Definition{Fields: []Field{FileField("upload", 100)}})
	if _, err := s.Validate(context.Background(), parsed.Values, BindOptions{}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Representation(context.Background(), parsed.Values)
	if err != nil || len(out) != 0 {
		t.Fatal("upload pathname exposed", out, err)
	}
	s = mustSerializer(t, Definition{Fields: []Field{ImageField("upload", 100, 1000)}})
	if _, err := s.Validate(context.Background(), parsed.Values, BindOptions{}); err == nil {
		t.Fatal("invalid image accepted")
	}
	header := parsed.Values["upload"].(*multipart.FileHeader)
	header.Filename = "../outside"
	s = mustSerializer(t, Definition{Fields: []Field{FileField("upload", 100)}})
	if _, err := s.Validate(context.Background(), parsed.Values, BindOptions{}); err == nil {
		t.Fatal("traversal filename accepted")
	}
}
