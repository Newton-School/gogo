package syndication

import (
	"context"
	"html"
	"io"
	"strconv"
	"strings"
	"time"
)

func (RSS2) ContentType() string { return "application/rss+xml; charset=utf-8" }

// Encode writes RSS 2.0 without interpreting any metadata as XML. Plain
// descriptions are also HTML-escaped because RSS readers interpret description
// text as HTML. Explicitly trusted HTML remains XML character data, not markup.
func (RSS2) Encode(ctx context.Context, dst io.Writer, feed Feed) error {
	if err := feedEncodeInput(ctx, dst); err != nil {
		return err
	}
	if feed.Title == "" || feed.Link == "" {
		return ErrInvalidFeed
	}
	for _, item := range feed.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(item.Enclosures) > 1 || (item.Title == "" && item.Description.Value == "") {
			return ErrInvalidFeed
		}
	}
	x := newFeedXML(ctx, dst)
	if x.err != nil {
		return x.err
	}
	x.start("rss", feedXMLAttr("version", "2.0"), feedXMLAttr("xmlns:atom", atomNamespace))
	x.start("channel")
	x.element("title", feed.Title)
	x.element("link", feed.Link)
	x.element("description", rssDescription(feed.Description))
	if feed.FeedURL != "" {
		x.element("atom:link", "", feedXMLAttr("rel", "self"), feedXMLAttr("href", feed.FeedURL), feedXMLAttr("type", "application/rss+xml"))
	}
	x.optional("language", feed.Language)
	x.optional("copyright", feed.Copyright)
	x.optional("generator", feed.Generator)
	rssAuthor(x, "managingEditor", feed.Author)
	if !feed.Updated.IsZero() {
		x.element("lastBuildDate", feed.Updated.UTC().Format(time.RFC1123Z))
	}
	rssCategories(x, feed.Categories)
	for _, item := range feed.Items {
		if x.err != nil {
			return x.err
		}
		x.start("item")
		x.optional("title", item.Title)
		x.optional("link", item.Link)
		x.element("description", rssDescription(item.Description))
		rssAuthor(x, "author", item.Author)
		if !item.Published.IsZero() {
			x.element("pubDate", item.Published.UTC().Format(time.RFC1123Z))
		}
		x.optional("comments", item.Comments)
		x.optional("guid", item.ID, feedXMLAttr("isPermaLink", strconv.FormatBool(item.IDIsPermalink)))
		for _, enclosure := range item.Enclosures {
			x.element("enclosure", "", feedXMLAttr("url", enclosure.URL), feedXMLAttr("length", strconv.FormatInt(enclosure.Length, 10)), feedXMLAttr("type", enclosure.MIMEType))
		}
		rssCategories(x, item.Categories)
		x.end("item")
	}
	x.end("channel")
	x.end("rss")
	return x.finish()
}

func rssDescription(content Content) string {
	if content.HTML {
		return content.Value
	}
	return html.EscapeString(content.Value)
}

func rssAuthor(x *feedXML, element string, author *Author) {
	if author == nil {
		return
	}
	if author.Email != "" {
		value := author.Email
		if author.Name != "" {
			// Escape RFC 5322 comment syntax independently of XML escaping.
			name := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(author.Name)
			value += " (" + name + ")"
		}
		x.element(element, value)
	} else if author.Name != "" {
		x.element("dc:creator", author.Name, feedXMLAttr("xmlns:dc", dublinCoreNamespace))
	}
}

func rssCategories(x *feedXML, categories []Category) {
	for _, category := range categories {
		if category.Scheme == "" {
			x.element("category", category.Term)
		} else {
			x.element("category", category.Term, feedXMLAttr("domain", category.Scheme))
		}
	}
}
