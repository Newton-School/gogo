package sitemaps

import (
	"context"
	"encoding/xml"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sitemapRenderConfig() Config {
	return Config{Origin: "https://example.test", AdditionalOrigins: []string{"https://es.example.test"}, Policy: Policy{
		Entry:     func(context.Context, Entry) error { return nil },
		Alternate: func(context.Context, Entry, Alternate) error { return nil },
		Index:     func(context.Context, IndexEntry) error { return nil },
	}}
}

func sitemapRenderEntry() Entry {
	priority := 0.75
	return Entry{Loc: "/en/article?x=1&y=2", LastMod: LastModified{Date: "2026-09-08"}, ChangeFreq: "weekly", Priority: &priority, Alternates: []Alternate{
		{Language: "en", Loc: "/en/article?x=1&y=2"},
		{Language: "es", Loc: "https://es.example.test/article"},
		{Language: "x-default", Loc: "/en/article?x=1&y=2"},
	}}
}

func newSitemapRenderTest(t *testing.T, config Config) *Renderer {
	t.Helper()
	r, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type sitemapParsedURL struct {
	Loc        string  `xml:"loc"`
	LastMod    string  `xml:"lastmod"`
	ChangeFreq string  `xml:"changefreq"`
	Priority   *string `xml:"priority"`
	Alternates []struct {
		XMLName  xml.Name
		Rel      string `xml:"rel,attr"`
		Language string `xml:"hreflang,attr"`
		Loc      string `xml:"href,attr"`
	} `xml:"http://www.w3.org/1999/xhtml link"`
}

func sitemapParsePage(t *testing.T, document Document) []sitemapParsedURL {
	t.Helper()
	if !strings.HasPrefix(string(document.Body), xml.Header) || document.ContentType != "application/xml; charset=utf-8" || len(document.ETag) != 66 {
		t.Fatalf("document metadata: %s %q", document.ContentType, document.ETag)
	}
	var page struct {
		XMLName xml.Name
		URLs    []sitemapParsedURL `xml:"url"`
	}
	if err := xml.Unmarshal(document.Body, &page); err != nil {
		t.Fatal(err)
	}
	if page.XMLName != (xml.Name{Space: "http://www.sitemaps.org/schemas/sitemap/0.9", Local: "urlset"}) {
		t.Fatal(page.XMLName)
	}
	return page.URLs
}

func requireSitemapError(t *testing.T, document Document, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || document.Body != nil || document.ContentType != "" || document.ETag != "" {
		t.Fatalf("got document=%+v error=%v, want %v", document, err, want)
	}
}

func TestSitemapRenderPageMetadataNamespacesAndOwnership(t *testing.T) {
	r := newSitemapRenderTest(t, sitemapRenderConfig())
	entry := sitemapRenderEntry()
	document, err := r.RenderPage(context.Background(), []Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	items := sitemapParsePage(t, document)
	if len(items) != 1 || items[0].Loc != "https://example.test/en/article?x=1&y=2" || items[0].LastMod != "2026-09-08" || items[0].ChangeFreq != "weekly" || items[0].Priority == nil || *items[0].Priority != "0.75" {
		t.Fatalf("wrong page metadata: %+v", items)
	}
	if len(items[0].Alternates) != 3 {
		t.Fatal(items[0].Alternates)
	}
	for _, alternate := range items[0].Alternates {
		if alternate.Rel != "alternate" || alternate.XMLName.Space != "http://www.w3.org/1999/xhtml" || !strings.HasPrefix(alternate.Loc, "https://") {
			t.Fatal(alternate)
		}
	}
	if !strings.Contains(string(document.Body), "x=1&amp;y=2") {
		t.Fatal("URL was not XML escaped")
	}
	if entry.Loc != "/en/article?x=1&y=2" || entry.Alternates[0].Loc != entry.Loc || *entry.Priority != 0.75 {
		t.Fatal("renderer mutated input")
	}
	repeated, err := r.RenderPage(context.Background(), []Entry{entry})
	if err != nil || !reflect.DeepEqual(document, repeated) {
		t.Fatal("render is not deterministic", err)
	}
	document.Body[0] = 'x'
	third, err := r.RenderPage(context.Background(), []Entry{entry})
	if err != nil || !reflect.DeepEqual(third, repeated) {
		t.Fatal("returned document borrows mutable bytes", err)
	}
}

func TestSitemapRenderOptionalZeroPriorityInstantsAndIndex(t *testing.T) {
	r := newSitemapRenderTest(t, sitemapRenderConfig())
	zero := 0.0
	small := 0.000000001
	at := time.Date(2026, 9, 8, 18, 0, 1, 123, time.FixedZone("fixture", 19800))
	document, err := r.RenderPage(context.Background(), []Entry{{Loc: "/zero", Priority: &zero}, {Loc: "/absent"}, {Loc: "/instant", Priority: &small, LastMod: LastModified{Time: at}}})
	if err != nil {
		t.Fatal(err)
	}
	items := sitemapParsePage(t, document)
	if len(items) != 3 || items[0].Priority == nil || *items[0].Priority != "0" || items[1].Priority != nil || items[1].LastMod != "" || items[2].Priority == nil || *items[2].Priority != "0.000000001" || items[2].LastMod != at.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("optional metadata mismatch: %+v", items)
	}
	index, err := r.RenderIndex(context.Background(), []IndexEntry{{Loc: "/sitemap-one.xml", LastMod: LastModified{Date: "2026-09-07"}}, {Loc: "/sitemap-two.xml", LastMod: LastModified{Time: at}}, {Loc: "/sitemap-three.xml"}})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		XMLName xml.Name
		Items   []struct {
			Loc     string `xml:"loc"`
			LastMod string `xml:"lastmod"`
		} `xml:"sitemap"`
	}
	if err := xml.Unmarshal(index.Body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.XMLName != (xml.Name{Space: "http://www.sitemaps.org/schemas/sitemap/0.9", Local: "sitemapindex"}) || len(parsed.Items) != 3 || parsed.Items[0].Loc != "https://example.test/sitemap-one.xml" || parsed.Items[0].LastMod != "2026-09-07" || parsed.Items[1].LastMod != at.UTC().Format(time.RFC3339Nano) || parsed.Items[2].LastMod != "" {
		t.Fatalf("index metadata mismatch: %+v", parsed)
	}
}

func TestSitemapRenderRequiresOperationGrants(t *testing.T) {
	config := sitemapRenderConfig()
	config.Policy.Index = nil
	r := newSitemapRenderTest(t, config)
	doc, err := r.RenderIndex(context.Background(), nil)
	requireSitemapError(t, doc, err, ErrInvalid)
	config = sitemapRenderConfig()
	config.Policy.Entry = nil
	r = newSitemapRenderTest(t, config)
	doc, err = r.RenderPage(context.Background(), nil)
	requireSitemapError(t, doc, err, ErrInvalid)
	if docs, err := r.BuildPages(context.Background(), nil); !errors.Is(err, ErrInvalid) || docs != nil {
		t.Fatal(docs, err)
	}
	config = sitemapRenderConfig()
	config.Policy.Alternate = nil
	r = newSitemapRenderTest(t, config)
	doc, err = r.RenderPage(context.Background(), []Entry{sitemapRenderEntry()})
	requireSitemapError(t, doc, err, ErrNotPublic)
	if _, err := r.RenderPage(context.Background(), []Entry{{Loc: "/without-alternates"}}); err != nil {
		t.Fatal(err)
	}
}

func TestSitemapRenderPublicationFailuresAreAtomic(t *testing.T) {
	for _, mode := range []string{"entry-denied", "alternate-denied", "index-denied", "mixed-error", "private-error", "panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			config := sitemapRenderConfig()
			want := ErrNotPublic
			callback := func() error {
				switch mode {
				case "mixed-error":
					return errors.Join(ErrNotPublic, errors.New("private provider detail"))
				case "private-error":
					return errors.New("private provider detail")
				case "panic":
					panic("private provider detail")
				case "cancel":
					cancel()
					return ErrNotPublic
				}
				return ErrNotPublic
			}
			if mode == "alternate-denied" {
				config.Policy.Alternate = func(context.Context, Entry, Alternate) error { return callback() }
			} else {
				config.Policy.Entry = func(context.Context, Entry) error { return callback() }
			}
			if mode == "mixed-error" || mode == "private-error" || mode == "panic" {
				want = ErrUnavailable
			}
			if mode == "cancel" {
				want = context.Canceled
			}
			r := newSitemapRenderTest(t, config)
			var doc Document
			var err error
			if mode == "index-denied" {
				config.Policy.Index = func(context.Context, IndexEntry) error { return callback() }
				r = newSitemapRenderTest(t, config)
				doc, err = r.RenderIndex(ctx, []IndexEntry{{Loc: "/sitemap.xml"}})
			} else {
				doc, err = r.RenderPage(ctx, []Entry{sitemapRenderEntry()})
			}
			requireSitemapError(t, doc, err, want)
			if strings.Contains(err.Error(), "private") {
				t.Fatal("provider detail exposed", err)
			}
		})
	}
}

func TestSitemapRenderRejectsCurrentCallbackMutations(t *testing.T) {
	for _, mode := range []string{"priority", "alternates", "time-location", "alternate-view", "index-time-location"} {
		t.Run(mode, func(t *testing.T) {
			originalUTC := *time.UTC
			defer func() { *time.UTC = originalUTC }()
			entry := sitemapRenderEntry()
			config := sitemapRenderConfig()
			config.Policy.Entry = func(_ context.Context, value Entry) error {
				switch mode {
				case "priority":
					*value.Priority = 0.25
				case "alternates":
					value.Alternates[0].Loc = "/retargeted"
				case "time-location":
					*value.LastMod.Time.Location() = *time.FixedZone("mutated", 3600)
				}
				return nil
			}
			config.Policy.Alternate = func(_ context.Context, value Entry, _ Alternate) error {
				if mode == "alternate-view" {
					*value.Priority = 0.25
				}
				return nil
			}
			config.Policy.Index = func(_ context.Context, value IndexEntry) error {
				*value.LastMod.Time.Location() = *time.FixedZone("mutated", 3600)
				return nil
			}
			r := newSitemapRenderTest(t, config)
			var doc Document
			var err error
			if mode == "index-time-location" {
				doc, err = r.RenderIndex(context.Background(), []IndexEntry{{Loc: "/index.xml"}})
			} else {
				doc, err = r.RenderPage(context.Background(), []Entry{entry})
			}
			requireSitemapError(t, doc, err, ErrInvalid)
			if *entry.Priority != 0.75 || entry.Alternates[0].Loc != entry.Loc || !reflect.DeepEqual(*time.UTC, originalUTC) {
				t.Fatal("callback mutation reached caller or global data")
			}
		})
	}
}

func TestSitemapRenderFreezesWholeInputAndRendererBeforeCallbacks(t *testing.T) {
	config := sitemapRenderConfig()
	entries := []Entry{sitemapRenderEntry(), {Loc: "/second"}}
	var retained Entry
	var renderer *Renderer
	replacementConfig := sitemapRenderConfig()
	replacementConfig.Policy.Entry = func(context.Context, Entry) error { return ErrNotPublic }
	replacement := newSitemapRenderTest(t, replacementConfig)
	calls := 0
	config.Policy.Entry = func(_ context.Context, entry Entry) error {
		calls++
		if calls == 1 {
			retained = entry
			entries[1].Loc = "/caller-mutated"
			*renderer = *replacement
		} else {
			*retained.Priority = 0.1
			retained.Alternates[0].Loc = "/retained-mutated"
		}
		return nil
	}
	renderer = newSitemapRenderTest(t, config)
	doc, err := renderer.RenderPage(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	items := sitemapParsePage(t, doc)
	if len(items) != 2 || items[1].Loc != "https://example.test/second" || *items[0].Priority != "0.75" || strings.Contains(string(doc.Body), "mutated") || calls != 2 {
		t.Fatal("snapshot changed during callbacks", items, calls)
	}
	doc, err = renderer.RenderPage(context.Background(), []Entry{{Loc: "/next"}})
	requireSitemapError(t, doc, err, ErrNotPublic)
}

type sitemapNilContext struct{ context.Context }

func TestSitemapRenderLimitsInvalidArgumentsAndDuplicatesBeforeCallbacks(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.MaxItems = -1 }, func(c *Config) { c.MaxItems = 50001 }, func(c *Config) { c.MaxBytes = 1023 }, func(c *Config) { c.MaxBytes = (50 << 20) + 1 },
		func(c *Config) { c.MaxPages = -1 }, func(c *Config) { c.MaxPages = 1001 }, func(c *Config) { c.MaxBuildBytes = 1023 }, func(c *Config) { c.MaxBuildBytes = (128 << 20) + 1 },
	} {
		config := sitemapRenderConfig()
		change(&config)
		if r, err := New(config); r != nil || !errors.Is(err, ErrLimit) {
			t.Fatal(r, err)
		}
	}
	config := sitemapRenderConfig()
	config.Policy = Policy{}
	if r, err := New(config); r != nil || !errors.Is(err, ErrInvalid) {
		t.Fatal(r, err)
	}
	calls := 0
	config = sitemapRenderConfig()
	config.MaxItems = 1
	config.Policy.Entry = func(context.Context, Entry) error { calls++; return nil }
	config.Policy.Index = func(context.Context, IndexEntry) error { calls++; return nil }
	r := newSitemapRenderTest(t, config)
	doc, err := r.RenderPage(context.Background(), []Entry{{Loc: "/one"}, {Loc: "/two"}})
	requireSitemapError(t, doc, err, ErrLimit)
	doc, err = r.RenderIndex(context.Background(), []IndexEntry{{Loc: "/one.xml"}, {Loc: "/two.xml"}})
	requireSitemapError(t, doc, err, ErrLimit)
	if calls != 0 {
		t.Fatal("overflow invoked publication callback")
	}
	for _, ctx := range []context.Context{nil, (*sitemapNilContext)(nil)} {
		doc, err := r.RenderPage(ctx, nil)
		requireSitemapError(t, doc, err, ErrInvalid)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doc, err = r.RenderPage(ctx, nil)
	requireSitemapError(t, doc, err, context.Canceled)
	var missing *Renderer
	doc, err = missing.RenderPage(context.Background(), nil)
	requireSitemapError(t, doc, err, ErrInvalid)
	config.MaxItems = 2
	r = newSitemapRenderTest(t, config)
	doc, err = r.RenderPage(context.Background(), []Entry{{Loc: "/one"}, {Loc: "https://example.test/one"}})
	requireSitemapError(t, doc, err, ErrInvalid)
	doc, err = r.RenderIndex(context.Background(), []IndexEntry{{Loc: "/one.xml"}, {Loc: "https://example.test/one.xml"}})
	requireSitemapError(t, doc, err, ErrInvalid)
	if calls != 0 {
		t.Fatal("duplicate location invoked callback")
	}
}
