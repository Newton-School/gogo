package syndication

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type formatXMLNode struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Text     string
	Children []*formatXMLNode
}

func parseFormatXML(t *testing.T, data []byte) *formatXMLNode {
	t.Helper()
	if !bytes.HasPrefix(data, []byte(xml.Header)) {
		t.Fatal("missing UTF-8 XML declaration")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var root *formatXMLNode
	var stack []*formatXMLNode
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid XML: %v", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			node := &formatXMLNode{Name: value.Name, Attrs: value.Attr}
			if len(stack) == 0 {
				if root != nil {
					t.Fatal("multiple root elements")
				}
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, node)
			}
			stack = append(stack, node)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(value)
			}
		}
	}
	if root == nil || len(stack) != 0 {
		t.Fatal("missing or incomplete root")
	}
	return root
}

func (n *formatXMLNode) children(namespace, name string) []*formatXMLNode {
	var result []*formatXMLNode
	for _, child := range n.Children {
		if child.Name.Space == namespace && child.Name.Local == name {
			result = append(result, child)
		}
	}
	return result
}

func (n *formatXMLNode) one(t *testing.T, namespace, name string) *formatXMLNode {
	t.Helper()
	children := n.children(namespace, name)
	if len(children) != 1 {
		t.Fatalf("%s: expected one {%s}%s, got %d", n.Name.Local, namespace, name, len(children))
	}
	return children[0]
}

func (n *formatXMLNode) attr(name string) string {
	for _, attr := range n.Attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func formatFixture() Feed {
	at := time.Date(2026, 9, 8, 15, 30, 1, 123, time.FixedZone("fixture", 19800))
	return Feed{
		ID: "urn:feed:immutable", Title: `Feed <title> & "quote"`, Link: "https://example.test/news?x=1&y=2", FeedURL: "https://example.test/feed?x=1&y=2",
		Description: Content{Value: `<b>not markup</b> & "text"`}, Language: "en", Copyright: "Feed © <owner>", Generator: "Gogo & Generator",
		Author: &Author{Name: "Feed <author>", Email: "feed@example.test", URL: "https://example.test/authors/feed"}, Updated: at,
		Categories: []Category{{Term: "framework & tools", Scheme: "https://example.test/categories", Label: `Label "quoted"`}},
		Items: []Item{{
			ID: "urn:item:immutable", Title: "Item <title>", Link: "https://example.test/posts/one", Description: Content{Value: `<em>plain</em> & text`},
			Author: &Author{Name: "Editor (one)\\two", Email: "editor@example.test", URL: "https://example.test/editors/one"}, Published: at.Add(-time.Hour), Updated: at,
			Categories: []Category{{Term: "backend & Go", Scheme: "https://example.test/topics", Label: "Backend <label>"}},
			Enclosures: []Enclosure{{URL: "https://example.test/media?x=1&y=2", Length: 42, MIMEType: "audio/ogg"}}, Comments: "https://example.test/posts/one/comments", Copyright: "Item © author",
		}},
	}
}

func encodeFormat(t *testing.T, format Format, feed Feed) *formatXMLNode {
	t.Helper()
	var output bytes.Buffer
	if err := format.Encode(context.Background(), &output, feed); err != nil {
		t.Fatal(err)
	}
	return parseFormatXML(t, output.Bytes())
}

func TestRSS2MetadataAndTextSafety(t *testing.T) {
	feed := formatFixture()
	root := encodeFormat(t, RSS2{}, feed)
	if root.Name != (xml.Name{Local: "rss"}) || root.attr("version") != "2.0" {
		t.Fatalf("wrong RSS root: %+v", root)
	}
	channel := root.one(t, "", "channel")
	if got := channel.one(t, "", "title"); got.Text != feed.Title || len(got.Children) != 0 {
		t.Fatal("title markup interpreted")
	}
	if got := channel.one(t, "", "description"); got.Text != "&lt;b&gt;not markup&lt;/b&gt; &amp; &#34;text&#34;" || len(got.Children) != 0 {
		t.Fatalf("unsafe plain description: %+v", got)
	}
	self := channel.one(t, atomNamespace, "link")
	if self.attr("href") != feed.FeedURL || self.attr("rel") != "self" || self.attr("type") != "application/rss+xml" {
		t.Fatal("self link mismatch")
	}
	if channel.one(t, "", "lastBuildDate").Text != feed.Updated.UTC().Format(time.RFC1123Z) {
		t.Fatal("feed update mismatch")
	}
	if channel.one(t, "", "managingEditor").Text != "feed@example.test (Feed <author>)" {
		t.Fatal("feed author mismatch")
	}
	for name, want := range map[string]string{"language": feed.Language, "copyright": feed.Copyright, "generator": feed.Generator, "link": feed.Link} {
		if channel.one(t, "", name).Text != want {
			t.Fatalf("%s mismatch", name)
		}
	}
	category := channel.one(t, "", "category")
	if category.Text != feed.Categories[0].Term || category.attr("domain") != feed.Categories[0].Scheme {
		t.Fatal("feed category mismatch")
	}
	item := channel.one(t, "", "item")
	if item.one(t, "", "guid").Text != feed.Items[0].ID || item.one(t, "", "guid").attr("isPermaLink") != "false" {
		t.Fatal("opaque ID not preserved")
	}
	if got := item.one(t, "", "author").Text; got != `editor@example.test (Editor \(one\)\\two)` {
		t.Fatalf("mail comment syntax not escaped: %q", got)
	}
	if item.one(t, "", "pubDate").Text != feed.Items[0].Published.UTC().Format(time.RFC1123Z) {
		t.Fatal("published date mismatch")
	}
	if item.one(t, "", "comments").Text != feed.Items[0].Comments {
		t.Fatal("comments mismatch")
	}
	enclosure := item.one(t, "", "enclosure")
	if enclosure.attr("url") != feed.Items[0].Enclosures[0].URL || enclosure.attr("length") != "42" || enclosure.attr("type") != "audio/ogg" {
		t.Fatal("enclosure mismatch")
	}
	if item.one(t, "", "category").Text != feed.Items[0].Categories[0].Term {
		t.Fatal("item category mismatch")
	}
}

func TestRSS2TrustedHTMLAuthorFallbackAndPermalink(t *testing.T) {
	feed := formatFixture()
	feed.Description.HTML = true
	feed.Author.Email = ""
	feed.Items[0].Description.HTML = true
	feed.Items[0].Author.Email = ""
	feed.Items[0].ID = feed.Items[0].Link
	feed.Items[0].IDIsPermalink = true
	channel := encodeFormat(t, RSS2{}, feed).one(t, "", "channel")
	if got := channel.one(t, "", "description"); got.Text != feed.Description.Value || len(got.Children) != 0 {
		t.Fatal("trusted content was not XML character data")
	}
	if channel.one(t, dublinCoreNamespace, "creator").Text != feed.Author.Name {
		t.Fatal("feed creator fallback missing")
	}
	item := channel.one(t, "", "item")
	if item.one(t, dublinCoreNamespace, "creator").Text != feed.Items[0].Author.Name || len(item.children("", "author")) != 0 {
		t.Fatal("item creator fallback mismatch")
	}
	if item.one(t, "", "guid").attr("isPermaLink") != "true" {
		t.Fatal("permalink flag missing")
	}
	if got := item.one(t, "", "description"); got.Text != feed.Items[0].Description.Value || len(got.Children) != 0 {
		t.Fatal("trusted item content became XML")
	}
}

func TestAtom1MetadataAndTextSafety(t *testing.T) {
	feed := formatFixture()
	feed.Items[0].Description.HTML = true
	feed.Items[0].Enclosures = append(feed.Items[0].Enclosures, Enclosure{URL: "https://example.test/media/two", Length: 0, MIMEType: "video/webm"})
	root := encodeFormat(t, Atom1{}, feed)
	if root.Name != (xml.Name{Space: atomNamespace, Local: "feed"}) {
		t.Fatalf("wrong Atom root: %+v", root.Name)
	}
	if root.attr("lang") != "en" {
		t.Fatal("language missing")
	}
	for _, child := range root.Children {
		if child.Name.Space != atomNamespace {
			t.Fatalf("lost Atom namespace: %+v", child.Name)
		}
	}
	if root.one(t, atomNamespace, "id").Text != feed.ID {
		t.Fatal("immutable feed ID changed")
	}
	if root.one(t, atomNamespace, "updated").Text != feed.Updated.UTC().Format(time.RFC3339Nano) {
		t.Fatal("feed update mismatch")
	}
	subtitle := root.one(t, atomNamespace, "subtitle")
	if subtitle.attr("type") != "text" || subtitle.Text != feed.Description.Value || len(subtitle.Children) != 0 {
		t.Fatal("plain subtitle interpreted")
	}
	if root.one(t, atomNamespace, "title").attr("type") != "text" {
		t.Fatal("title type missing")
	}
	person := root.one(t, atomNamespace, "author")
	if person.one(t, atomNamespace, "name").Text != feed.Author.Name || person.one(t, atomNamespace, "email").Text != feed.Author.Email || person.one(t, atomNamespace, "uri").Text != feed.Author.URL {
		t.Fatal("person fields mismatch")
	}
	links := root.children(atomNamespace, "link")
	if len(links) != 2 || links[0].attr("rel") != "alternate" || links[0].attr("href") != feed.Link || links[1].attr("rel") != "self" || links[1].attr("href") != feed.FeedURL {
		t.Fatal("feed links mismatch")
	}
	category := root.one(t, atomNamespace, "category")
	if category.attr("term") != feed.Categories[0].Term || category.attr("scheme") != feed.Categories[0].Scheme || category.attr("label") != feed.Categories[0].Label {
		t.Fatal("category fields mismatch")
	}
	if root.one(t, atomNamespace, "rights").Text != feed.Copyright || root.one(t, atomNamespace, "generator").Text != feed.Generator {
		t.Fatal("optional metadata mismatch")
	}
	entry := root.one(t, atomNamespace, "entry")
	if entry.one(t, atomNamespace, "id").Text != feed.Items[0].ID {
		t.Fatal("immutable entry ID changed")
	}
	if entry.one(t, atomNamespace, "updated").Text != feed.Items[0].Updated.UTC().Format(time.RFC3339Nano) || entry.one(t, atomNamespace, "published").Text != feed.Items[0].Published.UTC().Format(time.RFC3339Nano) {
		t.Fatal("entry date mismatch")
	}
	summary := entry.one(t, atomNamespace, "summary")
	if summary.attr("type") != "html" || summary.Text != feed.Items[0].Description.Value || len(summary.Children) != 0 {
		t.Fatal("HTML summary interpreted as XML")
	}
	links = entry.children(atomNamespace, "link")
	if len(links) != 3 || links[1].attr("rel") != "enclosure" || links[1].attr("length") != "42" || links[1].attr("type") != "audio/ogg" || links[1].attr("href") != feed.Items[0].Enclosures[0].URL || links[2].attr("length") != "0" {
		t.Fatal("entry enclosure links mismatch")
	}
	if entry.one(t, atomNamespace, "rights").Text != feed.Items[0].Copyright {
		t.Fatal("entry rights missing")
	}
}

func TestFeedFormatsValidateRequirementsBeforeWriting(t *testing.T) {
	cases := []struct {
		name   string
		format Format
		change func(*Feed)
	}{
		{"rss title", RSS2{}, func(f *Feed) { f.Title = "" }},
		{"rss link", RSS2{}, func(f *Feed) { f.Link = "" }},
		{"rss item text", RSS2{}, func(f *Feed) { f.Items[0].Title = ""; f.Items[0].Description.Value = "" }},
		{"rss enclosures", RSS2{}, func(f *Feed) { f.Items[0].Enclosures = append(f.Items[0].Enclosures, f.Items[0].Enclosures[0]) }},
		{"atom id", Atom1{}, func(f *Feed) { f.ID = "" }},
		{"atom title", Atom1{}, func(f *Feed) { f.Title = "" }},
		{"atom updated", Atom1{}, func(f *Feed) { f.Updated = time.Time{} }},
		{"atom date range", Atom1{}, func(f *Feed) { f.Updated = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"atom feed author name", Atom1{}, func(f *Feed) { f.Author.Name = "" }},
		{"atom no author", Atom1{}, func(f *Feed) { f.Author = nil; f.Items[0].Author = nil }},
		{"atom empty no author", Atom1{}, func(f *Feed) { f.Author = nil; f.Items = nil }},
		{"atom entry id", Atom1{}, func(f *Feed) { f.Items[0].ID = "" }},
		{"atom entry title", Atom1{}, func(f *Feed) { f.Items[0].Title = "" }},
		{"atom entry link", Atom1{}, func(f *Feed) { f.Items[0].Link = "" }},
		{"atom entry updated", Atom1{}, func(f *Feed) { f.Items[0].Updated = time.Time{} }},
		{"atom entry date range", Atom1{}, func(f *Feed) { f.Items[0].Published = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"atom entry author name", Atom1{}, func(f *Feed) { f.Items[0].Author.Name = "" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			feed := formatFixture()
			test.change(&feed)
			var output bytes.Buffer
			if err := test.format.Encode(context.Background(), &output, feed); !errors.Is(err, ErrInvalidFeed) || output.Len() != 0 {
				t.Fatalf("err=%v output=%d", err, output.Len())
			}
		})
	}
}

func TestAtom1AuthorInheritanceAndOptionalFields(t *testing.T) {
	for _, feedAuthor := range []bool{false, true} {
		t.Run(map[bool]string{false: "entry authors", true: "feed inheritance"}[feedAuthor], func(t *testing.T) {
			feed := formatFixture()
			if feedAuthor {
				feed.Items[0].Author = nil
			} else {
				feed.Author = nil
			}
			feed.FeedURL = ""
			feed.Language = ""
			feed.Copyright = ""
			feed.Generator = ""
			feed.Categories = nil
			feed.Items[0].Published = time.Time{}
			feed.Items[0].Copyright = ""
			feed.Items[0].Enclosures = nil
			feed.Items[0].Categories = nil
			root := encodeFormat(t, Atom1{}, feed)
			entry := root.one(t, atomNamespace, "entry")
			if feedAuthor && len(entry.children(atomNamespace, "author")) != 0 {
				t.Fatal("invented entry author")
			}
			if !feedAuthor && len(root.children(atomNamespace, "author")) != 0 {
				t.Fatal("invented feed author")
			}
			if len(root.children(atomNamespace, "link")) != 1 || len(entry.children(atomNamespace, "published")) != 0 {
				t.Fatal("invented optional fields")
			}
		})
	}
	feed := formatFixture()
	feed.Items = nil
	encodeFormat(t, Atom1{}, feed)
	feed.Author = nil
	feed.Updated = time.Time{}
	encodeFormat(t, RSS2{}, feed)
}

func TestAtom1RequiresAbsoluteIRIsWithoutRewritingIdentifiers(t *testing.T) {
	valid := []string{
		"urn:item:immutable", "URN:Case%2fSensitive", "tag:example.test,2026:news/42",
		"https://例え.テスト/記事?lang=日本語#項目", "https://example.test/a%2Fb?x=1&y=2",
		"https://[2001:db8::1]:443/one", "https://[v1.a:b]/one", "file:///one", "custom:",
		"custom:one?private=\ue000", "custom:😀", "custom:one?#",
	}
	for _, id := range valid {
		feed := formatFixture()
		feed.ID, feed.Items[0].ID = id, id
		root := encodeFormat(t, Atom1{}, feed)
		if root.one(t, atomNamespace, "id").Text != id || root.one(t, atomNamespace, "entry").one(t, atomNamespace, "id").Text != id {
			t.Fatalf("IRI was rewritten: %q", id)
		}
	}
	invalid := []string{
		"", "opaque-id", "/relative", "//example.test/id", "1bad:one", "é:one",
		"urn:contains space", "urn:bad%", "urn:bad%2G", "urn:<tag>", "urn:back\\slash",
		"urn:one#two#three", "urn:one[other]", "https://user@@example.test/id", "https://[not-ip]/id",
		"https://[::1]oops/id", "https://[fe80::1%25zone]/id", "https://example.test:invalid/id",
		"https://[v.no-version]/id", "https://[v1.%20]/id", "urn:\ue000", "urn:one#\ue000", "urn:\ufffe", "urn:\U0001ffff", "urn:\u0085", "urn:\xff",
	}
	for _, id := range invalid {
		for _, entry := range []bool{false, true} {
			feed := formatFixture()
			if entry {
				feed.Items[0].ID = id
			} else {
				feed.ID = id
			}
			var output bytes.Buffer
			if err := (Atom1{}).Encode(context.Background(), &output, feed); !errors.Is(err, ErrInvalidFeed) || output.Len() != 0 {
				t.Fatalf("invalid IRI accepted (%t) %q: err=%v output=%d", entry, id, err, output.Len())
			}
		}
	}
	// RSS opaque GUIDs are a separate contract and must not be canonicalized or
	// rejected merely because the same text cannot serve as an Atom identifier.
	feed := formatFixture()
	feed.Items[0].ID = "opaque item identifier"
	item := encodeFormat(t, RSS2{}, feed).one(t, "", "channel").one(t, "", "item")
	if item.one(t, "", "guid").Text != feed.Items[0].ID {
		t.Fatal("RSS GUID semantics changed")
	}
}

func TestAtom1CategorySchemesMustBeIRIs(t *testing.T) {
	for _, scheme := range []string{"relative", "/category", "urn:bad%", "https://example.test/has space"} {
		for _, entry := range []bool{false, true} {
			feed := formatFixture()
			if entry {
				feed.Items[0].Categories[0].Scheme = scheme
			} else {
				feed.Categories[0].Scheme = scheme
			}
			var output bytes.Buffer
			if err := (Atom1{}).Encode(context.Background(), &output, feed); !errors.Is(err, ErrInvalidFeed) || output.Len() != 0 {
				t.Fatalf("invalid category scheme %q accepted: %v", scheme, err)
			}
			// RSS category domains are a separate, opaque vocabulary label.
			encodeFormat(t, RSS2{}, feed)
		}
	}
	feed := formatFixture()
	feed.Categories[0].Scheme = "urn:category:public"
	feed.Items[0].Categories[0].Scheme = "https://例え.テスト/分類"
	encodeFormat(t, Atom1{}, feed)
}

type formatTestWriter struct {
	bytes.Buffer
	write  func([]byte) (int, error)
	closed bool
}

func (w *formatTestWriter) Write(p []byte) (int, error) {
	if w.write != nil {
		return w.write(p)
	}
	return w.Buffer.Write(p)
}
func (w *formatTestWriter) Close() error { w.closed = true; return nil }

func TestFeedFormatsWriterAndCancellationBoundaries(t *testing.T) {
	for _, format := range []Format{RSS2{}, Atom1{}} {
		t.Run(format.ContentType(), func(t *testing.T) {
			feed := formatFixture()
			for _, invalid := range []struct {
				ctx context.Context
				dst io.Writer
			}{{nil, &bytes.Buffer{}}, {context.Background(), nil}} {
				if err := format.Encode(invalid.ctx, invalid.dst, feed); !errors.Is(err, ErrInvalidFeed) {
					t.Fatalf("invalid arguments: %v", err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var untouched bytes.Buffer
			if err := format.Encode(ctx, &untouched, feed); !errors.Is(err, context.Canceled) || untouched.Len() != 0 {
				t.Fatal("canceled input wrote output")
			}
			sentinel := errors.New("writer failed")
			for _, failAt := range []int{1, 2} {
				calls := 0
				writer := &formatTestWriter{write: func(p []byte) (int, error) {
					calls++
					if calls == failAt {
						return 0, sentinel
					}
					return len(p), nil
				}}
				if err := format.Encode(context.Background(), writer, feed); err != sentinel || writer.closed {
					t.Fatalf("write %d: err=%v closed=%v", failAt, err, writer.closed)
				}
			}
			short := &formatTestWriter{write: func(p []byte) (int, error) { return len(p) - 1, nil }}
			if err := format.Encode(context.Background(), short, feed); !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("short write: %v", err)
			}
			for _, cancelAt := range []int{1, 2} {
				ctx, cancel := context.WithCancel(context.Background())
				calls := 0
				writer := &formatTestWriter{write: func(p []byte) (int, error) {
					calls++
					if calls == cancelAt {
						cancel()
					}
					return len(p), nil
				}}
				if err := format.Encode(ctx, writer, feed); !errors.Is(err, context.Canceled) || writer.closed {
					t.Fatalf("late cancel %d: %v", cancelAt, err)
				}
				cancel()
			}
			writer := &formatTestWriter{}
			if err := format.Encode(context.Background(), writer, feed); err != nil || writer.closed {
				t.Fatalf("writer closed: %v", err)
			}
		})
	}
}

func TestFeedFormatsDeterministicAndDoNotMutateMetadata(t *testing.T) {
	for _, format := range []Format{RSS2{}, Atom1{}} {
		feed := formatFixture()
		before, err := json.Marshal(feed)
		if err != nil {
			t.Fatal(err)
		}
		var first, second bytes.Buffer
		if err := format.Encode(context.Background(), &first, feed); err != nil {
			t.Fatal(err)
		}
		if err := format.Encode(context.Background(), &second, feed); err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(feed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) || !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Fatal("metadata mutated or output unstable")
		}
		if !strings.Contains(format.ContentType(), "charset=utf-8") {
			t.Fatal("encoding missing from content type")
		}
	}
}

func FuzzFeedFormatsPreserveEscapedContent(f *testing.F) {
	for _, seed := range []string{`</summary><script>alert("x")</script>`, `]]> & <tag>`, "Go café 世界 😀", "line\r\nnext\tvalue"} {
		f.Add(seed, false)
		f.Add(seed, true)
	}
	f.Fuzz(func(t *testing.T, value string, trustedHTML bool) {
		if len(value) > 2048 || !utf8.ValidString(value) {
			t.Skip()
		}
		for _, r := range value {
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xfffe || r == 0xffff {
				t.Skip()
			}
		}
		feed := formatFixture()
		feed.Description = Content{Value: value, HTML: trustedHTML}
		feed.Items[0].Description = feed.Description
		feed.Categories[0].Term = value
		feed.Categories[0].Label = value
		for _, format := range []Format{RSS2{}, Atom1{}} {
			root := encodeFormat(t, format, feed)
			var descriptions []*formatXMLNode
			want := value
			if _, rss := format.(RSS2); rss {
				channel := root.one(t, "", "channel")
				descriptions = []*formatXMLNode{channel.one(t, "", "description"), channel.one(t, "", "item").one(t, "", "description")}
				if !trustedHTML {
					want = html.EscapeString(value)
				}
			} else {
				descriptions = []*formatXMLNode{root.one(t, atomNamespace, "subtitle"), root.one(t, atomNamespace, "entry").one(t, atomNamespace, "summary")}
				category := root.one(t, atomNamespace, "category")
				if category.attr("term") != value || category.attr("label") != value {
					t.Fatal("attribute data changed")
				}
				mode := "text"
				if trustedHTML {
					mode = "html"
				}
				for _, description := range descriptions {
					if description.attr("type") != mode {
						t.Fatal("wrong text construct type")
					}
				}
			}
			for _, description := range descriptions {
				if description.Text != want || len(description.Children) != 0 {
					t.Fatalf("content interpreted or changed: %q != %q", description.Text, want)
				}
			}
		}
	})
}
