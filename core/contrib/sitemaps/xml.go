package sitemaps

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"strconv"
)

// Only these constant document frames are literal XML. Every metadata value
// below is encoded through encoding/xml, never inserted into raw markup.
const (
	sitemapPageOpen   = xml.Header + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">`
	sitemapPageClose  = `</urlset>`
	sitemapIndexOpen  = xml.Header + `<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`
	sitemapIndexClose = `</sitemapindex>`
)

type sitemapBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *sitemapBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.buffer.Len() {
		return 0, ErrLimit
	}
	return b.buffer.Write(data)
}
func (b *sitemapBuffer) Bytes() []byte { return b.buffer.Bytes() }
func (b *sitemapBuffer) Len() int      { return b.buffer.Len() }

type sitemapWriter struct {
	ctx context.Context
	dst io.Writer
}

func (w sitemapWriter) Write(data []byte) (int, error) {
	if err := sitemapContextError(w.ctx); err != nil {
		return 0, err
	}
	n, err := w.dst.Write(data)
	if err != nil {
		return n, err
	}
	if n != len(data) {
		return n, io.ErrShortWrite
	}
	return n, sitemapContextError(w.ctx)
}

type sitemapXML struct {
	ctx     context.Context
	encoder *xml.Encoder
	err     error
}

func newSitemapXML(ctx context.Context, dst io.Writer) *sitemapXML {
	return &sitemapXML{ctx: ctx, encoder: xml.NewEncoder(sitemapWriter{ctx: ctx, dst: dst})}
}

func (x *sitemapXML) token(token xml.Token) {
	if x.err != nil {
		return
	}
	if x.err = sitemapContextError(x.ctx); x.err == nil {
		x.err = x.encoder.EncodeToken(token)
	}
}
func (x *sitemapXML) start(name string, attrs ...xml.Attr) {
	x.token(xml.StartElement{Name: xml.Name{Local: name}, Attr: attrs})
}
func (x *sitemapXML) end(name string) { x.token(xml.EndElement{Name: xml.Name{Local: name}}) }
func (x *sitemapXML) element(name, value string) {
	x.start(name)
	x.token(xml.CharData(value))
	x.end(name)
}
func (x *sitemapXML) optional(name, value string) {
	if value != "" {
		x.element(name, value)
	}
}
func (x *sitemapXML) finish() error {
	if x.err != nil {
		return x.err
	}
	if err := sitemapContextError(x.ctx); err != nil {
		return err
	}
	if err := x.encoder.Flush(); err != nil {
		return err
	}
	return sitemapContextError(x.ctx)
}

func sitemapAttr(name, value string) xml.Attr {
	return xml.Attr{Name: xml.Name{Local: name}, Value: value}
}

func writeSitemapEntry(ctx context.Context, dst io.Writer, entry Entry) error {
	modified, err := lastModValue(entry.LastMod)
	if err != nil {
		return err
	}
	x := newSitemapXML(ctx, dst)
	x.start("url")
	x.element("loc", entry.Loc)
	x.optional("lastmod", modified)
	x.optional("changefreq", entry.ChangeFreq)
	if entry.Priority != nil {
		// The sitemap schema uses decimal, not exponent notation.
		x.element("priority", strconv.FormatFloat(*entry.Priority, 'f', -1, 64))
	}
	for _, alternate := range entry.Alternates {
		x.start("xhtml:link", sitemapAttr("rel", "alternate"), sitemapAttr("hreflang", alternate.Language), sitemapAttr("href", alternate.Loc))
		x.end("xhtml:link")
	}
	x.end("url")
	return x.finish()
}

func writeSitemapIndexEntry(ctx context.Context, dst io.Writer, entry IndexEntry) error {
	modified, err := lastModValue(entry.LastMod)
	if err != nil {
		return err
	}
	x := newSitemapXML(ctx, dst)
	x.start("sitemap")
	x.element("loc", entry.Loc)
	x.optional("lastmod", modified)
	x.end("sitemap")
	return x.finish()
}
