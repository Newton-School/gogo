package fieldlab_test

import (
	"strings"
	"testing"

	"example.com/gogo-showcase/apps/fieldlab"
	"github.com/Newton-School/gogo/core/forms"
)

func TestAllSupportedWidgetsRenderAndRejectUnsafeAttributes(t *testing.T) {
	cases := fieldlab.WidgetCases()
	if len(cases) != 21 {
		t.Fatalf("widget inventory changed: %d", len(cases))
	}
	for _, example := range cases {
		t.Run(example.Name, func(t *testing.T) {
			bound := forms.BoundField{Field: example.Field, Name: example.Field.Name, ID: "id_" + example.Field.Name, Value: example.Value}
			html, err := example.Widget.Render(bound)
			if err != nil || html == "" {
				t.Fatalf("render: %q %v", html, err)
			}
			if example.Name == "password" && strings.Contains(string(html), "Example") {
				t.Fatal("password value was redisplayed")
			}
		})
	}
	if html, err := fieldlab.RenderWidgets(); err != nil || html == "" {
		t.Fatalf("combined widget gallery: %v", err)
	}
	field := forms.NewField("safe", forms.Char)
	bound := forms.BoundField{Field: field, Name: "safe", ID: "id_safe", Value: `<script>alert(1)</script>`}
	if html, err := (forms.InputWidget{Type: "text"}).Render(bound); err != nil || strings.Contains(string(html), "<script>") {
		t.Fatal("untrusted text was not escaped")
	}
	if _, err := (forms.InputWidget{Type: "text", Attrs: map[string]string{"onclick": "alert(1)"}}).Render(bound); err == nil {
		t.Fatal("inline event handler accepted")
	}
	for _, unsupported := range []string{"clearable-file", "select-date"} {
		if _, err := (forms.InputWidget{Type: unsupported}).Render(bound); err == nil {
			t.Fatalf("unsupported widget %s was silently accepted", unsupported)
		}
	}
}

func TestMediaMergeDeduplicatesWhilePreservingOrder(t *testing.T) {
	first := forms.Media{CSS: []string{"forms.css"}, JS: []string{"forms.js"}}
	merged := first.Merge(forms.Media{CSS: []string{"forms.css", "fieldlab.css"}, JS: []string{"forms.js"}})
	if len(merged.CSS) != 2 || merged.CSS[0] != "forms.css" || merged.CSS[1] != "fieldlab.css" || len(merged.JS) != 1 {
		t.Fatal("media was not deduplicated deterministically")
	}
}
