package syndication

import (
	"context"
	"encoding/xml"
	"io"
)

const (
	atomNamespace       = "http://www.w3.org/2005/Atom"
	dublinCoreNamespace = "http://purl.org/dc/elements/1.1/"
)

// feedXMLWriter checks cancellation at the actual buffered output boundary.
// It deliberately does not implement io.StringWriter, which could bypass Write.
type feedXMLWriter struct {
	ctx context.Context
	dst io.Writer
}

func (w feedXMLWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.dst.Write(p)
	if err != nil {
		return n, err
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, w.ctx.Err()
}

type feedXML struct {
	ctx context.Context
	enc *xml.Encoder
	err error
}

func newFeedXML(ctx context.Context, dst io.Writer) *feedXML {
	w := feedXMLWriter{ctx: ctx, dst: dst}
	_, err := io.WriteString(w, xml.Header)
	return &feedXML{ctx: ctx, enc: xml.NewEncoder(w), err: err}
}

func (x *feedXML) token(token xml.Token) {
	if x.err != nil {
		return
	}
	if x.err = x.ctx.Err(); x.err == nil {
		x.err = x.enc.EncodeToken(token)
	}
}

func (x *feedXML) start(name string, attrs ...xml.Attr) {
	x.token(xml.StartElement{Name: xml.Name{Local: name}, Attr: attrs})
}

func (x *feedXML) end(name string) {
	x.token(xml.EndElement{Name: xml.Name{Local: name}})
}

func (x *feedXML) element(name, value string, attrs ...xml.Attr) {
	x.start(name, attrs...)
	x.token(xml.CharData(value))
	x.end(name)
}

func (x *feedXML) optional(name, value string, attrs ...xml.Attr) {
	if value != "" {
		x.element(name, value, attrs...)
	}
}

func (x *feedXML) finish() error {
	if x.err != nil {
		return x.err
	}
	if err := x.ctx.Err(); err != nil {
		return err
	}
	if err := x.enc.Flush(); err != nil {
		return err
	}
	return x.ctx.Err()
}

func feedXMLAttr(name, value string) xml.Attr {
	return xml.Attr{Name: xml.Name{Local: name}, Value: value}
}

func feedEncodeInput(ctx context.Context, dst io.Writer) error {
	if ctx == nil || dst == nil {
		return ErrInvalidFeed
	}
	return ctx.Err()
}
