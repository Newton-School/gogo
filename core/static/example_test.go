package static_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/Newton-School/gogo/core/static"
	"github.com/Newton-School/gogo/core/templates"
)

func ExampleNew() {
	// Production projects can use embedded assets or an explicit Directory.
	parent, err := os.MkdirTemp("", "gogo-static-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(parent)
	collector, err := static.New(static.Config{
		Sources:     []static.Source{{Owner: "project", FS: fstest.MapFS{"css/app.css": {Data: []byte("body{color:navy}")}}}},
		Destination: filepath.Join(parent, "public"), BaseURL: "/static/",
	})
	if err != nil {
		panic(err)
	}
	report, err := collector.Collect(context.Background(), static.CollectOptions{DryRun: true})
	if err != nil {
		panic(err)
	}
	url, err := report.Manifest.URL("css/app.css")
	if err != nil {
		panic(err)
	}
	engine := templates.New(templates.Config{Tags: report.Manifest.Tags(), Libraries: []string{"static"}})
	markup, err := engine.RenderString(context.Background(), `{% load static %}<link href="{% static "css/app.css" %}">`, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println(report.DryRun, report.Published, len(report.Manifest.Assets()))
	fmt.Println(strings.HasPrefix(url, "/static/css/"), strings.Contains(markup, url))
	// Output:
	// true false 1
	// true true
}
