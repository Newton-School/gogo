package urls

import (
	"errors"
	"net/http"
	"reflect"
	"testing"
)

type descriptionTripwire struct{}

func (*descriptionTripwire) ServeHTTP(http.ResponseWriter, *http.Request) {
	panic("describing routes must not invoke a handler")
}

func TestDescriptionCapturesFlattenedOrderAndDetachesMethods(t *testing.T) {
	handler := &descriptionTripwire{}
	methods := []string{"GET", "HEAD"}
	router, err := New(Include("api/", "v1",
		Include("books/", "catalog", Path("<uuid:id>/", handler, "detail", methods...)),
		RePath("status$", handler, "status", "GET"),
	))
	if err != nil {
		t.Fatal(err)
	}
	methods[0] = "POST"
	description, err := router.Describe(2)
	if err != nil || !description.BuiltinConverters || len(description.Routes) != 2 {
		t.Fatal(description, err)
	}
	want := []Route{
		{Pattern: "/api/books/<uuid:id>/", Name: "v1:catalog:detail", Namespace: "v1:catalog", Methods: []string{"GET", "HEAD"}, Handler: handler},
		{Pattern: "/api/status$", Name: "v1:status", Namespace: "v1", Methods: []string{"GET"}, Handler: handler, Regex: true},
	}
	if !reflect.DeepEqual(description.Routes, want) || !reflect.DeepEqual(description.Routes, router.Routes()) {
		t.Fatalf("description differs from compiled inventory: %#v", description)
	}
	description.Routes[0].Pattern = "/retargeted/"
	description.Routes[0].Name = "retargeted"
	description.Routes[0].Namespace = "retargeted"
	description.Routes[0].Methods[0] = "DELETE"
	description.Routes[0].Handler = nil
	description.Routes[0].Children = []Route{{Name: "unexpected"}}
	description.BuiltinConverters = false
	again, err := router.Describe(2)
	if err != nil || !again.BuiltinConverters || !reflect.DeepEqual(again.Routes, want) {
		t.Fatal("description mutation changed router", again, err)
	}
	legacy := router.Routes()
	legacy[0].Methods[0] = "PATCH"
	if !reflect.DeepEqual(again.Routes, want) {
		t.Fatal("descriptions share route method storage")
	}
}

func TestDescriptionReportsConstructorProvenanceWithoutCallbacks(t *testing.T) {
	handler := &descriptionTripwire{}
	custom := Builtins()
	custom["str"] = Converter{
		Pattern: `[^/]+`,
		Decode:  func(string) (any, error) { panic("description called converter Decode") },
		Encode:  func(any) (string, error) { panic("description called converter Encode") },
	}
	for _, test := range []struct {
		name    string
		factory func() (*Router, error)
		builtin bool
	}{
		{"standard constructor", func() (*Router, error) { return New(Path("<str:id>/", handler, "detail", "GET")) }, true},
		{"explicit builtin map", func() (*Router, error) {
			return NewWithConverters(Builtins(), Path("<str:id>/", handler, "detail", "GET"))
		}, false},
		{"custom builtin override", func() (*Router, error) { return NewWithConverters(custom, Path("<str:id>/", handler, "detail", "GET")) }, false},
		{"empty standard router", func() (*Router, error) { return New() }, true},
		{"zero router", func() (*Router, error) { return &Router{}, nil }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, err := test.factory()
			if err != nil {
				t.Fatal(err)
			}
			original, err := router.Describe(4096)
			if err != nil || original.BuiltinConverters != test.builtin || !reflect.DeepEqual(original.Routes, router.Routes()) {
				t.Fatal(original, err)
			}
			withFallback, err := router.WithNotFound(handler)
			if err != nil {
				t.Fatal(err)
			}
			copy, err := withFallback.Describe(4096)
			if err != nil || !reflect.DeepEqual(copy, original) {
				t.Fatal("fallback changed route description/provenance", copy, err)
			}
		})
	}
}

func TestDescriptionRejectsInvalidAndOverBudgetRequestsWithoutPartialData(t *testing.T) {
	handler := &descriptionTripwire{}
	router, err := New(Path("one/", handler, "one", "GET"), Path("two/", handler, "two", "GET"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		router *Router
		limit  int
	}{{nil, 1}, {router, -1}, {router, 0}, {router, 4097}, {router, 1}} {
		description, err := test.router.Describe(test.limit)
		if !errors.Is(err, ErrDescription) || description.Routes != nil || description.BuiltinConverters {
			t.Fatal("invalid description returned partial metadata", description, err)
		}
	}
	if description, err := router.Describe(2); err != nil || len(description.Routes) != 2 {
		t.Fatal("exact route boundary rejected", description, err)
	}
	// New remains unchanged: repeated method entries are valid runtime metadata.
	// Describe alone enforces its aggregate copy budget before allocating output.
	methods := make([]string, 8192)
	for i := range methods {
		methods[i] = "GET"
	}
	router, err = New(Path("one/", handler, "one", methods...), Path("two/", handler, "two", methods...))
	if err != nil {
		t.Fatal(err)
	}
	if description, err := router.Describe(2); err != nil || len(description.Routes[0].Methods)+len(description.Routes[1].Methods) != 16384 {
		t.Fatal("exact aggregate method boundary rejected", err)
	}
	router, err = New(Path("one/", handler, "one", methods...), Path("two/", handler, "two", append(methods, "HEAD")...))
	if err != nil {
		t.Fatal("description bound changed runtime constructor behavior", err)
	}
	if description, err := router.Describe(2); !errors.Is(err, ErrDescription) || description.Routes != nil || description.BuiltinConverters {
		t.Fatal("aggregate methods exceeded budget without refusing whole result", description, err)
	}
}
