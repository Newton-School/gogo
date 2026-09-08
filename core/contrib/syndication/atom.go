package syndication

import (
	"context"
	"encoding/xml"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (Atom1) ContentType() string { return "application/atom+xml; charset=utf-8" }

// Encode writes Atom 1.0 text constructs. Description is an Atom summary; each
// entry therefore has an alternate link rather than embedding fetched content.
func (Atom1) Encode(ctx context.Context, dst io.Writer, feed Feed) error {
	if err := feedEncodeInput(ctx, dst); err != nil {
		return err
	}
	if !atomIDValid(feed.ID) || feed.Title == "" || !atomDateValid(feed.Updated) || !atomAuthorValid(feed.Author) || !atomCategorySchemesValid(feed.Categories) || (feed.Author == nil && len(feed.Items) == 0) {
		return ErrInvalidFeed
	}
	for _, item := range feed.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !atomIDValid(item.ID) || item.Title == "" || item.Link == "" || !atomDateValid(item.Updated) || !atomAuthorValid(item.Author) || !atomCategorySchemesValid(item.Categories) || (feed.Author == nil && item.Author == nil) {
			return ErrInvalidFeed
		}
		if !item.Published.IsZero() && !atomDateValid(item.Published) {
			return ErrInvalidFeed
		}
	}
	x := newFeedXML(ctx, dst)
	if x.err != nil {
		return x.err
	}
	attrs := []xml.Attr{feedXMLAttr("xmlns", atomNamespace)}
	if feed.Language != "" {
		attrs = append(attrs, feedXMLAttr("xml:lang", feed.Language))
	}
	x.start("feed", attrs...)
	atomText(x, "title", Content{Value: feed.Title})
	atomLink(x, "alternate", feed.Link, "")
	atomLink(x, "self", feed.FeedURL, "application/atom+xml")
	x.element("id", feed.ID)
	x.element("updated", feed.Updated.UTC().Format(time.RFC3339Nano))
	atomAuthor(x, feed.Author)
	atomText(x, "subtitle", feed.Description)
	atomCategories(x, feed.Categories)
	if feed.Copyright != "" {
		atomText(x, "rights", Content{Value: feed.Copyright})
	}
	x.optional("generator", feed.Generator)
	for _, item := range feed.Items {
		if x.err != nil {
			return x.err
		}
		x.start("entry")
		atomText(x, "title", Content{Value: item.Title})
		atomLink(x, "alternate", item.Link, "")
		x.element("id", item.ID)
		x.element("updated", item.Updated.UTC().Format(time.RFC3339Nano))
		if !item.Published.IsZero() {
			x.element("published", item.Published.UTC().Format(time.RFC3339Nano))
		}
		atomAuthor(x, item.Author)
		atomText(x, "summary", item.Description)
		for _, enclosure := range item.Enclosures {
			x.element("link", "", feedXMLAttr("rel", "enclosure"), feedXMLAttr("href", enclosure.URL), feedXMLAttr("length", strconv.FormatInt(enclosure.Length, 10)), feedXMLAttr("type", enclosure.MIMEType))
		}
		atomCategories(x, item.Categories)
		if item.Copyright != "" {
			atomText(x, "rights", Content{Value: item.Copyright})
		}
		x.end("entry")
	}
	x.end("feed")
	return x.finish()
}

func atomDateValid(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() >= 1 && at.UTC().Year() <= 9999
}

func atomAuthorValid(author *Author) bool { return author == nil || author.Name != "" }

func atomCategorySchemesValid(categories []Category) bool {
	for _, category := range categories {
		if category.Scheme != "" && !atomIDValid(category.Scheme) {
			return false
		}
	}
	return true
}

// Atom IDs are absolute IRIs, not necessarily dereferenceable HTTP URLs. Check
// RFC 3987 syntax without normalization: case, escaping and Unicode spelling are
// part of the immutable identifier and must survive byte-for-byte (RFC 4287).
func atomIDValid(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	colon := strings.IndexByte(value, ':')
	if colon < 1 {
		return false
	}
	for i, b := range []byte(value[:colon]) {
		if !(b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || i > 0 && (b >= '0' && b <= '9' || b == '+' || b == '-' || b == '.')) {
			return false
		}
	}
	rest := value[colon+1:]
	if before, fragment, found := strings.Cut(rest, "#"); found {
		if !atomIRIComponent(fragment, ":@/?", false) {
			return false
		}
		rest = before
	}
	if before, query, found := strings.Cut(rest, "?"); found {
		if !atomIRIComponent(query, ":@/?", true) {
			return false
		}
		rest = before
	}
	if strings.HasPrefix(rest, "//") {
		authority, path, _ := strings.Cut(rest[2:], "/")
		if !atomIRIAuthority(authority) {
			return false
		}
		rest = path
	}
	return atomIRIComponent(rest, ":@/", false)
}

func atomIRIAuthority(authority string) bool {
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		if !atomIRIComponent(authority[:at], ":", false) {
			return false
		}
		authority = authority[at+1:]
	}
	var port string
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return false
		}
		literal, tail := authority[1:end], authority[end+1:]
		addr, err := netip.ParseAddr(literal)
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			// RFC 3986 also permits an IPvFuture literal: v<hex>.<address>.
			version, address, ok := strings.Cut(literal, ".")
			if !ok || len(version) < 2 || version[0] != 'v' && version[0] != 'V' || address == "" {
				return false
			}
			for _, b := range []byte(version[1:]) {
				if !atomIRIHex(b) {
					return false
				}
			}
			for _, b := range []byte(address) {
				if b >= 128 || b == '%' {
					return false
				}
			}
			if !atomIRIComponent(address, ":", false) {
				return false
			}
		}
		if tail != "" {
			if tail[0] != ':' {
				return false
			}
			port = tail[1:]
		}
	} else {
		host := authority
		if colon := strings.LastIndexByte(authority, ':'); colon >= 0 {
			host, port = authority[:colon], authority[colon+1:]
		}
		if !atomIRIComponent(host, "", false) {
			return false
		}
	}
	for _, b := range []byte(port) {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}

func atomIRIComponent(value, extra string, private bool) bool {
	for i := 0; i < len(value); {
		b := value[i]
		if b == '%' {
			if i+2 >= len(value) || !atomIRIHex(value[i+1]) || !atomIRIHex(value[i+2]) {
				return false
			}
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		i += size
		if r < 128 {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~!$&'()*+,;="+extra, r)) {
				return false
			}
			continue
		}
		ucs := r >= 0xa0 && r <= 0xd7ff || r >= 0xf900 && r <= 0xfdcf || r >= 0xfdf0 && r <= 0xffef || r >= 0x10000 && r <= 0xdfffd && r&0xffff <= 0xfffd || r >= 0xe1000 && r <= 0xefffd
		iprivate := r >= 0xe000 && r <= 0xf8ff || r >= 0xf0000 && r <= 0xffffd || r >= 0x100000 && r <= 0x10fffd
		if !ucs && !(private && iprivate) {
			return false
		}
	}
	return true
}

func atomIRIHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func atomText(x *feedXML, name string, content Content) {
	typeName := "text"
	if content.HTML {
		typeName = "html"
	}
	x.element(name, content.Value, feedXMLAttr("type", typeName))
}

func atomAuthor(x *feedXML, author *Author) {
	if author == nil {
		return
	}
	x.start("author")
	x.element("name", author.Name)
	x.optional("email", author.Email)
	x.optional("uri", author.URL)
	x.end("author")
}

func atomLink(x *feedXML, rel, href, mediaType string) {
	if href == "" {
		return
	}
	attrs := []xml.Attr{feedXMLAttr("rel", rel), feedXMLAttr("href", href)}
	if mediaType != "" {
		attrs = append(attrs, feedXMLAttr("type", mediaType))
	}
	x.element("link", "", attrs...)
}

func atomCategories(x *feedXML, categories []Category) {
	for _, category := range categories {
		attrs := []xml.Attr{feedXMLAttr("term", category.Term)}
		if category.Scheme != "" {
			attrs = append(attrs, feedXMLAttr("scheme", category.Scheme))
		}
		if category.Label != "" {
			attrs = append(attrs, feedXMLAttr("label", category.Label))
		}
		x.element("category", "", attrs...)
	}
}
