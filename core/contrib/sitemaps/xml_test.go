package sitemaps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type sitemapTestWriter struct {
	write  func([]byte) (int, error)
	calls  int
	closed bool
}

func (w *sitemapTestWriter) Write(p []byte) (int, error) { w.calls++; return w.write(p) }
func (w *sitemapTestWriter) Close() error                { w.closed = true; return nil }

func TestSitemapXMLPreservesWriterAndContextFailures(t *testing.T) {
	for _, index := range []bool{false, true} {
		for _, mode := range []string{"error", "short", "canceled", "cancel-during-write", "success"} {
			t.Run(mode+map[bool]string{false: "-page", true: "-index"}[index], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				providerErr := errors.New("test writer failure")
				var want error
				writer := &sitemapTestWriter{write: func(p []byte) (int, error) {
					switch mode {
					case "error":
						return 0, providerErr
					case "short":
						return len(p) - 1, nil
					case "cancel-during-write":
						cancel()
					}
					return len(p), nil
				}}
				switch mode {
				case "error":
					want = providerErr
				case "short":
					want = io.ErrShortWrite
				case "canceled":
					cancel()
					want = context.Canceled
				case "cancel-during-write":
					want = context.Canceled
				}
				var err error
				if index {
					err = writeSitemapIndexEntry(ctx, writer, IndexEntry{Loc: "https://example.test/index.xml"})
				} else {
					err = writeSitemapEntry(ctx, writer, Entry{Loc: "https://example.test/article"})
				}
				if !errors.Is(err, want) || writer.closed || (mode == "canceled" && writer.calls != 0) {
					t.Fatal(err, want, writer.calls, writer.closed)
				}
			})
		}
	}
}

func TestSitemapXMLEscapesValuesWithoutMarkupInterpolation(t *testing.T) {
	// Helpers encode values; validation is the separate renderer boundary.
	value := `https://example.test/?q=<script>"'&value`
	var output bytes.Buffer
	output.WriteString(sitemapPageOpen)
	if err := writeSitemapEntry(context.Background(), &output, Entry{Loc: value, Alternates: []Alternate{{Language: `en"<>&`, Loc: value}}}); err != nil {
		t.Fatal(err)
	}
	output.WriteString(sitemapPageClose)
	items := sitemapParsePage(t, sitemapDocument(output.Bytes()))
	if len(items) != 1 || items[0].Loc != value || len(items[0].Alternates) != 1 || items[0].Alternates[0].Loc != value || items[0].Alternates[0].Language != `en"<>&` {
		t.Fatal(items)
	}
	if strings.Contains(output.String(), "<script>") {
		t.Fatal("metadata became XML markup")
	}
}

func TestSitemapXMLBufferLimitDoesNotAppendPartialValue(t *testing.T) {
	buffer := &sitemapBuffer{limit: 5}
	if n, err := buffer.Write([]byte("123")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := buffer.Write([]byte("456")); n != 0 || !errors.Is(err, ErrLimit) || string(buffer.Bytes()) != "123" {
		t.Fatal(n, err, string(buffer.Bytes()))
	}
	if n, err := buffer.Write([]byte("45")); n != 2 || err != nil || string(buffer.Bytes()) != "12345" {
		t.Fatal(n, err)
	}
}
