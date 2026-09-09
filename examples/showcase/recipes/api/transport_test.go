package api_test

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/api"
)

func TestFileAndImageConstructorsWithBoundedMultipartParsing(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "example.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("sample text")); err != nil {
		t.Fatal(err)
	}
	picture, err := writer.CreateFormFile("image", "pixel.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(picture, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/uploads/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	parsed, err := api.Parse(request, api.ParseOptions{MaxBytes: 4096, MaxDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := parsed.Close(); err != nil {
			t.Error(err)
		}
	})
	s := serializer(t, api.FileField("file", 1024), api.ImageField("image", 1024, 16))
	if _, err := s.Validate(t.Context(), parsed.Values, api.BindOptions{}); err != nil {
		t.Fatal(err)
	}
	if output, err := s.Representation(t.Context(), parsed.Values); err != nil || len(output) != 0 {
		t.Fatal("write-only uploads must not expose paths or bytes")
	}
	// Parsing/validation did not publish a file; storage ownership is separate.
	imageInput := api.Values{"image": parsed.Values["file"]}
	if _, err := serializer(t, api.ImageField("image", 1024, 16)).Validate(t.Context(), imageInput, api.BindOptions{}); err == nil {
		t.Fatal("text masquerading as an image accepted")
	}
	for _, invalid := range []*multipart.FileHeader{{Filename: "../outside", Size: 1}, {Filename: "empty", Size: 0}, {Filename: "too-big", Size: 1025}} {
		if _, err := serializer(t, api.FileField("file", 1024)).Validate(t.Context(), api.Values{"file": invalid}, api.BindOptions{}); err == nil {
			t.Fatal("unsafe/empty/oversized upload accepted")
		}
	}
}

func TestJSONParserDuplicateDepthSizeMediaAndUnknownFieldDenial(t *testing.T) {
	for _, example := range []struct{ contentType, body string }{
		{"application/json", `{"count":1,"count":2}`},
		{"application/json", `{"count":1} trailing`},
		{"application/json", `{"count":[[[[[1]]]]]}`},
		{"application/json", strings.Repeat(" ", 257)},
		{"text/plain", `{"count":1}`},
	} {
		request := httptest.NewRequest("POST", "/api/records/", strings.NewReader(example.body))
		request.Header.Set("Content-Type", example.contentType)
		if parsed, err := api.Parse(request, api.ParseOptions{MaxBytes: 256, MaxDepth: 3}); err == nil {
			_ = parsed.Close()
			t.Fatalf("invalid input accepted: %s", example.contentType)
		}
	}
	request := httptest.NewRequest("POST", "/api/records/", strings.NewReader(`{"count":9007199254740993}`))
	request.Header.Set("Content-Type", "application/json")
	parsed, err := api.Parse(request, api.ParseOptions{MaxBytes: 256, MaxDepth: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer parsed.Close()
	values, err := serializer(t, api.IntegerField("count")).Validate(t.Context(), parsed.Values, api.BindOptions{})
	if err != nil || values["count"] != int64(9007199254740993) {
		t.Fatalf("parser precision: %v", err)
	}
	if _, err := api.Negotiate("text/html", "application/json"); err == nil {
		t.Fatal("unsupported response representation accepted")
	}
}
