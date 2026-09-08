package syndication

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"
)

func publicItem(context.Context, Item) error                 { return nil }
func publicEnclosure(context.Context, Item, Enclosure) error { return nil }

func sampleFeed() Feed {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	return Feed{Title: "Public updates", Link: "/", FeedURL: "/feed.xml", Description: Content{Value: "Latest releases"}, Updated: now, Author: &Author{Name: "Public author", Email: "editor@example.test", URL: "/authors/editor"}, Items: []Item{{Title: "A release", Link: "/releases/one", Description: Content{Value: "Plain <b>text</b> & data"}, Updated: now, Published: now.Add(-time.Hour), Categories: []Category{{Term: "release"}}, Enclosures: []Enclosure{{URL: "/public/audio.mp3", Length: 42, MIMEType: "audio/mpeg"}}}}}
}

func renderConfig() Config {
	return Config{Origin: "https://example.test", Policy: Policy{Item: publicItem, Enclosure: publicEnclosure}}
}

func TestRenderOwnsURLsInputAndConditionalMetadata(t *testing.T) {
	for _, format := range []Format{RSS2{}, Atom1{}} {
		cfg := renderConfig()
		cfg.Format = format
		r, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		feed := sampleFeed()
		doc, err := r.Render(context.Background(), feed)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(doc.Body), "https://example.test/releases/one") || doc.ETag == "" || !doc.LastModified.Equal(feed.Updated) || feed.Items[0].Link != "/releases/one" || feed.ID != "" || feed.Author.URL != "/authors/editor" {
			t.Fatal(doc, feed)
		}
		if err := validXML(context.Background(), doc.Body); err != nil {
			t.Fatal(err)
		}
		for _, item := range feed.Items {
			if !strings.Contains(string(doc.Body), xmlEscape(t, item.Title)) {
				t.Fatal("lost item")
			}
		}
		second, err := r.Render(context.Background(), feed)
		if err != nil || string(doc.Body) != string(second.Body) || doc.ETag != second.ETag {
			t.Fatal("nondeterministic render", err)
		}
		doc.Body[0] = 'x'
		*doc.LastModified.Location() = *time.FixedZone("changed", 3600)
		third, err := r.Render(context.Background(), feed)
		if err != nil || string(third.Body) != string(second.Body) || third.ETag != second.ETag {
			t.Fatal("returned document aliased renderer or input", err)
		}
	}
}

func xmlEscape(t *testing.T, value string) string {
	t.Helper()
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(value)); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestRenderRejectsUnsafeOriginsURLsAndMetadata(t *testing.T) {
	for _, origin := range []string{"", "//example.test", "javascript:bad", "https://user:pass@example.test", "https://example.test/a", "https://example.test?x=1", "https://example.test?", "https://example.test:", "https://example.test:0", "https://example.test:0655", "https://example.test:65536", "https://example.test:abc", "https://éxample.test", "https://bad..test", "https://bad-.test", "https://[fe80::1%25eth0]"} {
		cfg := renderConfig()
		cfg.Origin = origin
		if _, err := New(cfg); err == nil {
			t.Fatal("unsafe origin", origin)
		}
	}
	r, err := New(renderConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"javascript:alert(1)", "//other.test/a", "https://other.test/a", "/\\other.test/a", "/%00bad", "/%0abad", "/%5cbad", "/x?y=%0d", "relative", "https://example.test/a#%00"} {
		feed := sampleFeed()
		feed.Items[0].Link = link
		if doc, err := r.Render(context.Background(), feed); err == nil || doc.Body != nil {
			t.Fatal("unsafe link", link, err)
		}
	}
	for _, edit := range []func(*Feed){
		func(f *Feed) { f.Title = "" }, func(f *Feed) { f.Title = "a\x00b" }, func(f *Feed) { f.Title = string([]byte{255}) },
		func(f *Feed) { f.Items[0].Description.Value = strings.Repeat("a", (64<<10)+1) },
		func(f *Feed) { f.Updated = time.Time{} }, func(f *Feed) { f.Items[0].Updated = f.Updated.Add(time.Hour) },
		func(f *Feed) { f.Items[0].Published = f.Items[0].Updated.Add(time.Hour) },
		func(f *Feed) { f.Items = append(f.Items, f.Items[0]) }, func(f *Feed) { f.Author.Email = "private\n@example.test" },
		func(f *Feed) { f.Items[0].Enclosures[0].Length = -1 }, func(f *Feed) { f.Items[0].Enclosures[0].MIMEType = "audio/mpeg; name=secret" },
		func(f *Feed) { f.Items[0].Enclosures[0].URL = "/private?signature=secret" }, func(f *Feed) { f.Items[0].Enclosures[0].URL = "/private#secret" },
	} {
		feed := sampleFeed()
		edit(&feed)
		if doc, err := r.Render(context.Background(), feed); err == nil || doc.Body != nil {
			t.Fatal("invalid metadata returned document", feed, err)
		}
	}
	cfg := renderConfig()
	cfg.AdditionalOrigins = []string{"https://cdn.example.test"}
	r, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdditionalOrigins[0] = "https://other.test"
	feed := sampleFeed()
	feed.Items[0].Enclosures[0].URL = "https://cdn.example.test/a.mp3"
	if _, err := r.Render(context.Background(), feed); err != nil {
		t.Fatal(err)
	}
	feed.Link = "https://cdn.example.test/"
	if _, err := r.Render(context.Background(), feed); err == nil {
		t.Fatal("feed origin widened")
	}
	for raw, want := range map[string]string{"HTTPS://EXAMPLE.TEST.:443/": "https://example.test", "http://[2001:db8::1]:80": "http://[2001:db8::1]", "https://xn--bcher-kva.test": "https://xn--bcher-kva.test"} {
		u, err := parseOrigin(raw)
		if err != nil || u.String() != want {
			t.Fatal(raw, u, err)
		}
	}
}

func TestRenderEnclosuresRequireConcreteMediaTypes(t *testing.T) {
	r, err := New(renderConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, media := range []string{"audio", "*/*", "audio/*", "*/mpeg", "audio/mp*", "audio/mpeg/extra", "audio/mpeg; charset=utf-8"} {
		feed := sampleFeed()
		feed.Items[0].Enclosures[0].MIMEType = media
		if doc, err := r.Render(context.Background(), feed); !errors.Is(err, ErrInvalidFeed) || doc.Body != nil {
			t.Fatalf("invalid media type %q accepted: %v", media, err)
		}
	}
	for _, media := range []string{"audio/mpeg", "application/vnd.example.audio+json", "AUDIO/MPEG"} {
		feed := sampleFeed()
		feed.Items[0].Enclosures[0].MIMEType = media
		if _, err := r.Render(context.Background(), feed); err != nil {
			t.Fatalf("valid media type %q rejected: %v", media, err)
		}
	}
}

func TestRenderRSSAllowsEmailOnlyAuthorsButAtomRequiresNames(t *testing.T) {
	feed := sampleFeed()
	feed.Author = &Author{Email: "editor@example.test"}
	feed.Items[0].Author = &Author{Email: "writer@example.test"}
	r, err := New(renderConfig())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := r.Render(context.Background(), feed)
	if err != nil || !strings.Contains(string(doc.Body), "<author>writer@example.test</author>") || !strings.Contains(string(doc.Body), "<managingEditor>editor@example.test</managingEditor>") {
		t.Fatal("RSS email-only author rejected", err, string(doc.Body))
	}
	cfg := renderConfig()
	cfg.Format = Atom1{}
	r, _ = New(cfg)
	if doc, err := r.Render(context.Background(), feed); !errors.Is(err, ErrInvalidFeed) || doc.Body != nil {
		t.Fatal("Atom name requirement was weakened", err)
	}
}

func TestRenderPublicPoliciesAreDetachedReadOnlyAndOperationBound(t *testing.T) {
	cfg := renderConfig()
	feed := sampleFeed()
	cfg.Policy.Item = func(_ context.Context, item Item) error {
		item.Author = &Author{Name: "new"}
		item.Categories[0].Term = "mutated"
		return nil
	}
	r, _ := New(cfg)
	if doc, err := r.Render(context.Background(), feed); err == nil || doc.Body != nil || feed.Items[0].Categories[0].Term != "release" {
		t.Fatal("policy modified canonical fields", doc, err)
	}
	cfg.Policy.Item = func(_ context.Context, item Item) error {
		*item.Updated.Location() = *time.FixedZone("changed", 3600)
		return nil
	}
	r, _ = New(cfg)
	if _, err := r.Render(context.Background(), feed); !errors.Is(err, ErrInvalidFeed) {
		t.Fatal("mutable location escaped read-only guard", err)
	}
	feed.Items[0].Published = time.Time{}
	priorUTC := *time.UTC
	defer func() { *time.UTC = priorUTC }()
	cfg.Policy.Item = func(_ context.Context, item Item) error {
		if !item.Published.IsZero() {
			t.Fatal("missing publication time changed")
		}
		*item.Published.Location() = *time.FixedZone("changed", 3600)
		return nil
	}
	r, _ = New(cfg)
	if _, err := r.Render(context.Background(), feed); !errors.Is(err, ErrInvalidFeed) {
		t.Fatal(err)
	}
	_, offset := time.Now().In(time.UTC).Zone()
	if offset != 0 || !feed.Items[0].Published.IsZero() {
		t.Fatal("zero-time location exposed global state")
	}
	for _, policyErr := range []error{ErrNotPublic, errors.New("provider private failure"), errors.Join(ErrNotPublic, errors.New("provider failure"))} {
		cfg.Policy.Item = func(context.Context, Item) error { return policyErr }
		r, _ = New(cfg)
		doc, err := r.Render(context.Background(), feed)
		want := ErrUnavailable
		if policyErr == ErrNotPublic {
			want = ErrNotPublic
		}
		if doc.Body != nil || err != want {
			t.Fatal("public/error boundary", doc, err)
		}
	}
	cfg = renderConfig()
	cfg.Policy.Enclosure = nil
	r, _ = New(cfg)
	if _, err := r.Render(context.Background(), feed); !errors.Is(err, ErrNotPublic) {
		t.Fatal("enclosure authority omitted", err)
	}
	var renderer *Renderer
	replacementConfig := renderConfig()
	replacementConfig.Policy.Enclosure = func(context.Context, Item, Enclosure) error { return ErrNotPublic }
	replacement, _ := New(replacementConfig)
	cfg = renderConfig()
	cfg.Policy.Item = func(context.Context, Item) error { *renderer = *replacement; return nil }
	renderer, _ = New(cfg)
	if _, err := renderer.Render(context.Background(), feed); err != nil {
		t.Fatal("callback replaced current operation policy", err)
	}
	if _, err := renderer.Render(context.Background(), feed); !errors.Is(err, ErrNotPublic) {
		t.Fatal("next operation ignored new configuration", err)
	}
}

type testFormat struct {
	contentType string
	encode      func(context.Context, io.Writer, Feed) error
}

func (f testFormat) ContentType() string { return f.contentType }
func (f testFormat) Encode(ctx context.Context, w io.Writer, feed Feed) error {
	return f.encode(ctx, w, feed)
}

func TestRenderBoundsCustomFormatsAndFailureIsolation(t *testing.T) {
	for _, media := range []string{"text/html", "application/xhtml+xml", "application/rss+xml; charset=latin1", "application/xml; x=1", "application/xml\r\nX-Evil: yes"} {
		cfg := renderConfig()
		cfg.Format = testFormat{contentType: media}
		if _, err := New(cfg); err == nil {
			t.Fatal("unsafe custom content type", media)
		}
	}
	for _, body := range []string{"", "<x>", "<x/><y/>", `<!DOCTYPE x [<!ENTITY e SYSTEM "file:///private">]><x/>`, `<?xml-stylesheet href="https://example.test/x"?><x/>`, `<x a="1" a="2"/>`, strings.Repeat("<x>", 129) + strings.Repeat("</x>", 129)} {
		cfg := renderConfig()
		cfg.Format = testFormat{"application/xml", func(_ context.Context, w io.Writer, _ Feed) error { _, err := io.WriteString(w, body); return err }}
		r, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if doc, err := r.Render(context.Background(), sampleFeed()); err == nil || doc.Body != nil {
			t.Fatal("malformed/custom-active XML returned", body, err)
		}
	}
	for _, fn := range []func(context.Context, io.Writer, Feed) error{
		func(_ context.Context, w io.Writer, _ Feed) error {
			_, _ = io.WriteString(w, strings.Repeat("x", 2049))
			return nil
		},
		func(_ context.Context, w io.Writer, _ Feed) error {
			_, _ = w.Write([]byte(strings.Repeat("x", 2049)))
			return nil
		},
		func(_ context.Context, w io.Writer, _ Feed) error {
			_, _ = io.WriteString(w, "<x/>")
			return errors.New("provider private failure")
		},
		func(context.Context, io.Writer, Feed) error { panic("provider private failure") },
	} {
		cfg := renderConfig()
		cfg.MaxBytes = 2048
		cfg.Format = testFormat{"application/xml", fn}
		r, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if doc, err := r.Render(context.Background(), sampleFeed()); err == nil || doc.Body != nil || strings.Contains(err.Error(), "private") {
			t.Fatal(doc, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := renderConfig()
	cfg.Format = testFormat{"application/xml", func(_ context.Context, w io.Writer, _ Feed) error {
		cancel()
		_, err := io.WriteString(w, "<x/>")
		return err
	}}
	r, _ := New(cfg)
	if doc, err := r.Render(ctx, sampleFeed()); doc.Body != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("late cancellation returned feed", doc, err)
	}
	if _, err := r.Render(nil, sampleFeed()); err == nil {
		t.Fatal("nil context accepted")
	}
	cfg = renderConfig()
	cfg.MaxItems = 1
	r, _ = New(cfg)
	f := sampleFeed()
	f.Items = append(f.Items, f.Items[0])
	if _, err := r.Render(context.Background(), f); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}

func FuzzSyndicationURLBoundary(f *testing.F) {
	for _, s := range []string{"/a", "https://example.test/a", "//evil.test/a", "/%00", "/a?x=1", "/%2e%2e/private"} {
		f.Add(s)
	}
	r, err := New(renderConfig())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			return
		}
		result, err := r.link(raw, false, true)
		if err != nil && result != "" {
			t.Fatal("partial URL")
		}
		if err == nil {
			u, err := url.Parse(result)
			if err != nil || u.Scheme != "https" || u.Host != "example.test" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
				t.Fatal(result, err)
			}
		}
	})
}

func TestRenderXMLDeclarationAndOutsideRootLexicalBoundaries(t *testing.T) {
	for _, body := range []string{
		`<?xml nonsense?><x/>`, `<!--first--><?xml version="1.0"?><x/>`, `<?xml version="1.0" invalid="yes"?><x/>`,
		`<?xml encoding="utf-8" version="1.0"?><x/>`, `<?xml version="1.1"?><x/>`, `<?xml version="1.0" encoding="latin1"?><x/>`,
		"\u00a0<x/>", `<![CDATA[ ]]><x/>`, `&#32;<x/>`, `<x/>&#32;`, "<x/>\u00a0", " <?xml version=\"1.0\"?><x/>",
	} {
		if err := validXML(context.Background(), []byte(body)); err == nil {
			t.Fatal("malformed prolog/epilog", body)
		}
	}
	for _, body := range []string{`<?xml version="1.0"?><x/>`, `<?xml version = '1.0' encoding = 'UTF-8' standalone='yes'?><x/>`, "\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"utf-8\"?><x/>", " \t\r\n<!--before--><x/><!--after-->\r\n\t "} {
		if err := validXML(context.Background(), []byte(body)); err != nil {
			t.Fatal("valid UTF8 XML rejected", body, err)
		}
	}
}
