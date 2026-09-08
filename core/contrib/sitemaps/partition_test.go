package sitemaps

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func sitemapPartitionEntries(count int, escaped bool) []Entry {
	entries := make([]Entry, count)
	for i := range entries {
		entries[i].Loc = fmt.Sprintf("/article-%02d", i)
		if escaped {
			entries[i].Loc += "?" + strings.Repeat("x=1&", 200) + "end=1"
		}
	}
	return entries
}

func sitemapPartitionLocations(t *testing.T, documents []Document) []string {
	t.Helper()
	var result []string
	for _, document := range documents {
		for _, entry := range sitemapParsePage(t, document) {
			result = append(result, entry.Loc)
		}
	}
	return result
}

func TestSitemapBuildPagesStableCountPartitionAndOwnership(t *testing.T) {
	config := sitemapRenderConfig()
	config.MaxItems = 2
	r := newSitemapRenderTest(t, config)
	entries := sitemapPartitionEntries(5, false)
	documents, err := r.BuildPages(context.Background(), entries)
	if err != nil || len(documents) != 3 {
		t.Fatal(len(documents), err)
	}
	for i, expected := range []int{2, 2, 1} {
		if got := len(sitemapParsePage(t, documents[i])); got != expected {
			t.Fatalf("page %d has %d entries, want %d", i, got, expected)
		}
	}
	var want []string
	for _, entry := range entries {
		want = append(want, "https://example.test"+entry.Loc)
	}
	if got := sitemapPartitionLocations(t, documents); !reflect.DeepEqual(got, want) {
		t.Fatal(got, want)
	}
	repeated, err := r.BuildPages(context.Background(), entries)
	if err != nil || !reflect.DeepEqual(documents, repeated) {
		t.Fatal("unstable page output", err)
	}
	documents[0].Body[0] = 'x'
	if documents[1].Body[0] != '<' || repeated[0].Body[0] != '<' {
		t.Fatal("documents share mutable output")
	}
	empty, err := r.BuildPages(context.Background(), nil)
	if err != nil || len(empty) != 1 || len(sitemapParsePage(t, empty[0])) != 0 {
		t.Fatal(empty, err)
	}
}

func TestSitemapBuildPagesUsesExactEscapedBytesAndFrames(t *testing.T) {
	ctx := context.Background()
	entries := sitemapPartitionEntries(3, true)
	baseline := newSitemapRenderTest(t, sitemapRenderConfig())
	exact, err := baseline.RenderPage(ctx, entries[:2])
	if err != nil || len(exact.Body) < 1024 || !strings.Contains(string(exact.Body), "&amp;") {
		t.Fatal("invalid byte-boundary fixture", err)
	}
	config := sitemapRenderConfig()
	config.MaxBytes = len(exact.Body)
	r := newSitemapRenderTest(t, config)
	documents, err := r.BuildPages(ctx, entries)
	if err != nil || len(documents) != 2 || len(sitemapParsePage(t, documents[0])) != 2 || len(sitemapParsePage(t, documents[1])) != 1 || !reflect.DeepEqual(documents[0], exact) {
		t.Fatal("exact byte limit should fit two entries", len(documents), err)
	}
	for _, document := range documents {
		if len(document.Body) > config.MaxBytes {
			t.Fatal("document exceeds byte limit")
		}
	}
	config.MaxBytes--
	r = newSitemapRenderTest(t, config)
	documents, err = r.BuildPages(ctx, entries)
	if err != nil || len(documents) != 3 {
		t.Fatal("one byte below boundary should split every entry", len(documents), err)
	}
	config.MaxBytes = len(exact.Body)
	config.MaxItems = 1
	r = newSitemapRenderTest(t, config)
	if documents, err = r.BuildPages(ctx, entries); err != nil || len(documents) != 3 {
		t.Fatal("item limit must apply even if byte limit permits more", len(documents), err)
	}
	config.MaxBytes = 1024
	r = newSitemapRenderTest(t, config)
	if documents, err = r.BuildPages(ctx, entries); !errors.Is(err, ErrLimit) || documents != nil {
		t.Fatal("oversized single entry must not return partial pages", documents, err)
	}
}

func TestSitemapBuildPagesBoundsWholeOutputAndPageCount(t *testing.T) {
	ctx := context.Background()
	entries := sitemapPartitionEntries(3, true)
	config := sitemapRenderConfig()
	config.MaxItems = 1
	r := newSitemapRenderTest(t, config)
	documents, err := r.BuildPages(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	total, raw := 0, 0
	for _, document := range documents {
		total += len(document.Body)
	}
	for _, entry := range entries {
		n, err := entryInputSize(entry)
		if err != nil {
			t.Fatal(err)
		}
		raw += n
	}
	if total <= raw || total < 1024 {
		t.Fatal("fixture must exercise encoded output, not input preflight", raw, total)
	}
	config.MaxBuildBytes = total
	r = newSitemapRenderTest(t, config)
	exact, err := r.BuildPages(ctx, entries)
	if err != nil || !reflect.DeepEqual(documents, exact) {
		t.Fatal("exact whole-output budget did not fit", err)
	}
	config.MaxBuildBytes--
	r = newSitemapRenderTest(t, config)
	if documents, err := r.BuildPages(ctx, entries); !errors.Is(err, ErrLimit) || documents != nil {
		t.Fatal("whole-output overflow exposed completed earlier pages", documents, err)
	}
	config.MaxBuildBytes = 0
	config.MaxItems = 100
	config.MaxPages = 2
	// Each entry individually fits, but two escaped entries do not fit together.
	config.MaxBytes = len(exact[0].Body)
	r = newSitemapRenderTest(t, config)
	if documents, err := r.BuildPages(ctx, entries); !errors.Is(err, ErrLimit) || documents != nil {
		t.Fatal("actual page overflow exposed completed earlier pages", documents, err)
	}
}

func TestSitemapBuildPagesDenialCancellationAndDuplicatesAreAtomic(t *testing.T) {
	for _, mode := range []string{"denial", "panic", "cancel", "duplicate", "count"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entries := sitemapPartitionEntries(3, false)
			config := sitemapRenderConfig()
			config.MaxItems = 1
			calls, want := 0, ErrNotPublic
			config.Policy.Entry = func(context.Context, Entry) error {
				calls++
				if calls != 3 {
					return nil
				}
				switch mode {
				case "panic":
					panic("private failure")
				case "cancel":
					cancel()
				}
				return ErrNotPublic
			}
			switch mode {
			case "panic":
				want = ErrUnavailable
			case "cancel":
				want = context.Canceled
			case "duplicate":
				entries[2].Loc = "https://example.test" + entries[0].Loc
				want = ErrInvalid
			case "count":
				config.MaxPages = 2
				want = ErrLimit
			}
			r := newSitemapRenderTest(t, config)
			if documents, err := r.BuildPages(ctx, entries); !errors.Is(err, want) || documents != nil {
				t.Fatal(documents, err, want)
			}
			if (mode == "duplicate" || mode == "count") && calls != 0 {
				t.Fatal("preflight failure invoked callbacks", calls)
			}
		})
	}
}

func TestSitemapRenderRawAndEncodedLimitsAreIndependent(t *testing.T) {
	ctx := context.Background()
	config := sitemapRenderConfig()
	config.MaxBuildBytes = 1024
	calls := 0
	config.Policy.Entry = func(context.Context, Entry) error { calls++; return nil }
	config.Policy.Index = func(context.Context, IndexEntry) error { calls++; return nil }
	r := newSitemapRenderTest(t, config)
	entry := Entry{Loc: "/" + strings.Repeat("a", 1500)}
	doc, err := r.RenderPage(ctx, []Entry{entry})
	requireSitemapError(t, doc, err, ErrLimit)
	doc, err = r.RenderIndex(ctx, []IndexEntry{{Loc: entry.Loc}})
	requireSitemapError(t, doc, err, ErrLimit)
	if documents, err := r.BuildPages(ctx, []Entry{entry}); !errors.Is(err, ErrLimit) || documents != nil || calls != 0 {
		t.Fatal("raw budget should fail before grants", documents, err, calls)
	}
	config = sitemapRenderConfig()
	baseline := newSitemapRenderTest(t, config)
	indexEntries := []IndexEntry{{Loc: sitemapPartitionEntries(1, true)[0].Loc}}
	exact, err := baseline.RenderIndex(ctx, indexEntries)
	if err != nil || len(exact.Body) < 1024 {
		t.Fatal(err)
	}
	config.MaxBytes = len(exact.Body)
	r = newSitemapRenderTest(t, config)
	if doc, err := r.RenderIndex(ctx, indexEntries); err != nil || !reflect.DeepEqual(doc, exact) {
		t.Fatal("index exact output boundary", err)
	}
	config.MaxBytes--
	r = newSitemapRenderTest(t, config)
	doc, err = r.RenderIndex(ctx, indexEntries)
	requireSitemapError(t, doc, err, ErrLimit)
}
