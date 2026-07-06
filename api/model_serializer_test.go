package api

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/models"
)

func TestModelSerializerGeneratesFieldsAndValidatesConfig(t *testing.T) {
	meta := postMeta()
	serializer, err := NewModelSerializer(ModelSerializerConfig{
		Model:          meta,
		Fields:         []string{"id", "title", "author"},
		ReadOnlyFields: []string{"id"},
		ExtraKwargs:    map[string]FieldOptions{"title": {Required: true}},
	})
	if err != nil {
		t.Fatalf("NewModelSerializer() error = %v", err)
	}
	if got := serializer.FieldNames(); !reflect.DeepEqual(got, []string{"id", "title", "author"}) {
		t.Fatalf("FieldNames() = %#v", got)
	}
	validated, fieldErrors, ok := serializer.Validate(map[string]any{"id": 9, "title": "Gogo", "author": "7"})
	if !ok {
		t.Fatalf("Validate() errors = %#v", fieldErrors)
	}
	if !reflect.DeepEqual(validated, map[string]any{"author": int64(7), "title": "Gogo"}) {
		t.Fatalf("validated = %#v", validated)
	}

	if _, err := NewModelSerializer(ModelSerializerConfig{Model: meta, Fields: []string{"missing"}}); !errors.Is(err, ErrInvalidSerializerConfig) {
		t.Fatalf("invalid field error = %v, want ErrInvalidSerializerConfig", err)
	}
}

func TestModelSerializerCreateUpdateAndNestedRender(t *testing.T) {
	meta := postMeta()
	serializer, err := NewModelSerializer(ModelSerializerConfig{
		Model:          meta,
		Exclude:        []string{"internal"},
		ReadOnlyFields: []string{"id"},
		Depth:          1,
		NestedSerializers: map[string]*Serializer{
			"author": NewSerializer(StringField("name", FieldOptions{})),
		},
		CreateFunc: func(data map[string]any) (map[string]any, error) {
			data["id"] = int64(1)
			return data, nil
		},
		UpdateFunc: func(instance map[string]any, data map[string]any) (map[string]any, error) {
			for key, value := range data {
				instance[key] = value
			}
			return instance, nil
		},
	})
	if err != nil {
		t.Fatalf("NewModelSerializer() error = %v", err)
	}
	created, err := serializer.Create(map[string]any{"title": "Gogo", "author": int64(7)})
	if err != nil || created["id"] != int64(1) {
		t.Fatalf("Create() = %#v, %v", created, err)
	}
	updated, err := serializer.Update(map[string]any{"id": int64(1), "title": "Old"}, map[string]any{"title": "New"})
	if err != nil || updated["title"] != "New" {
		t.Fatalf("Update() = %#v, %v", updated, err)
	}
	rendered := serializer.Render(map[string]any{"id": int64(1), "title": "Gogo", "author": map[string]any{"name": "Saksham"}, "internal": "hidden"})
	if !reflect.DeepEqual(rendered, map[string]any{"author": map[string]any{"name": "Saksham"}, "id": int64(1), "title": "Gogo"}) {
		t.Fatalf("rendered = %#v", rendered)
	}
}

func postMeta() models.Metadata {
	return models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", PrimaryKey: true},
			{Name: "title"},
			{Name: "author", RelationTarget: "auth.User"},
			{Name: "internal"},
		},
	}
}
