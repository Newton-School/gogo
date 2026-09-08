package syndication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/url"
	"reflect"
	"regexp"
	"strings"
)

type Renderer struct {
	origin             *url.URL
	allowed            map[string]bool
	format             Format
	contentType        string
	policy             Policy
	maxItems, maxBytes int
}

func New(config Config) (renderer *Renderer, err error) {
	defer func() {
		if recover() != nil {
			renderer, err = nil, ErrUnavailable
		}
	}()
	origin, err := parseOrigin(config.Origin)
	if err != nil || config.Policy.Item == nil || len(config.AdditionalOrigins) > 64 {
		return nil, ErrInvalidFeed
	}
	if config.MaxItems == 0 {
		config.MaxItems = 100
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = 1 << 20
	}
	if config.MaxItems < 1 || config.MaxItems > 10000 || config.MaxBytes < 1024 || config.MaxBytes > 16<<20 {
		return nil, ErrLimit
	}
	if config.Format == nil {
		config.Format = RSS2{}
	}
	r := &Renderer{origin: origin, allowed: map[string]bool{origin.String(): true}, format: config.Format, policy: config.Policy, maxItems: config.MaxItems, maxBytes: config.MaxBytes}
	for _, raw := range config.AdditionalOrigins {
		other, err := parseOrigin(raw)
		if err != nil {
			return nil, ErrInvalidFeed
		}
		r.allowed[other.String()] = true
	}
	media, params, err := mime.ParseMediaType(config.Format.ContentType())
	if err != nil || media != "application/xml" && media != "text/xml" && media != "application/rss+xml" && media != "application/atom+xml" || len(params) > 1 || params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") {
		return nil, ErrInvalidFeed
	}
	for key := range params {
		if key != "charset" {
			return nil, ErrInvalidFeed
		}
	}
	r.contentType = mime.FormatMediaType(media, map[string]string{"charset": "utf-8"})
	return r, nil
}

// Render validates and detaches the whole feed, checks every public item and
// enclosure, and buffers one complete XML document before returning anything.
// Its validators never fetch URLs or read storage. Provider query scope and
// current publication state remain the application's trusted policy concern.
func (r *Renderer) Render(ctx context.Context, feed Feed) (document Document, err error) {
	defer func() {
		if recover() != nil {
			document, err = Document{}, ErrUnavailable
		}
		if ctx != nil && ctx.Err() != nil {
			document, err = Document{}, ctx.Err()
		}
		if err != nil {
			document = Document{}
		}
	}()
	if r == nil || ctx == nil {
		return Document{}, ErrInvalidFeed
	}
	operation := *r
	r = &operation
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	snapshot, err := r.normalize(ctx, feed)
	if err != nil {
		return Document{}, err
	}
	for _, item := range snapshot.Items {
		view := cloneItem(item)
		if err := r.policy.Item(ctx, view); err != nil {
			return Document{}, policyError(ctx, err)
		}
		if !reflect.DeepEqual(view, item) {
			return Document{}, ErrInvalidFeed
		}
		for _, enclosure := range item.Enclosures {
			if r.policy.Enclosure == nil {
				return Document{}, ErrNotPublic
			}
			view = cloneItem(item)
			if err := r.policy.Enclosure(ctx, view, enclosure); err != nil {
				return Document{}, policyError(ctx, err)
			}
			if !reflect.DeepEqual(view, item) {
				return Document{}, ErrInvalidFeed
			}
		}
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
	}
	// The formatter receives another owned copy: a retained policy view or
	// custom formatter can never alter the canonical metadata used below.
	writer := &boundedWriter{limit: r.maxBytes}
	if err := r.format.Encode(ctx, writer, cloneFeed(snapshot)); err != nil {
		if errors.Is(err, ErrLimit) {
			return Document{}, ErrLimit
		}
		if errors.Is(err, ErrInvalidFeed) {
			return Document{}, ErrInvalidFeed
		}
		return Document{}, ErrUnavailable
	}
	if writer.failed {
		return Document{}, ErrLimit
	}
	body := writer.Bytes()
	if err := validXML(ctx, body); err != nil {
		return Document{}, err
	}
	sum := sha256.Sum256(body)
	return Document{Body: append([]byte(nil), body...), ContentType: r.contentType, ETag: `"` + hex.EncodeToString(sum[:]) + `"`, LastModified: cloneTime(snapshot.Updated)}, nil
}

func policyError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == ErrNotPublic {
		return ErrNotPublic
	}
	return ErrUnavailable
}

type boundedWriter struct {
	buffer bytes.Buffer
	limit  int
	failed bool
}

func (w *boundedWriter) Bytes() []byte { return w.buffer.Bytes() }
func (w *boundedWriter) Len() int      { return w.buffer.Len() }

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.failed || len(p) > w.limit-w.Len() {
		w.failed = true
		return 0, ErrLimit
	}
	return w.buffer.Write(p)
}

var declarationPattern = regexp.MustCompile(`^version[ \t\r\n]*=[ \t\r\n]*(?:"1\.0"|'1\.0')(?:[ \t\r\n]+encoding[ \t\r\n]*=[ \t\r\n]*(?:"(?i:utf-8)"|'(?i:utf-8)'))?(?:[ \t\r\n]+standalone[ \t\r\n]*=[ \t\r\n]*(?:"yes"|'yes'|"no"|'no'))?[ \t\r\n]*$`)

func validXML(ctx context.Context, body []byte) error {
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	decoder := xml.NewDecoder(bytes.NewReader(body))
	depth, roots := 0, 0
	declaration := false
	for count := 0; ; count++ {
		if count > 1000000 {
			return ErrLimit
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		before := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ErrInvalidFeed
		}
		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
			if depth > 128 || roots > 1 {
				return ErrInvalidFeed
			}
			seen := map[xml.Name]bool{}
			for _, attr := range value.Attr {
				if seen[attr.Name] {
					return ErrInvalidFeed
				}
				seen[attr.Name] = true
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 {
				// The XML prolog/epilog accepts raw XML S only, not decoded
				// entities, CDATA sections or Unicode whitespace characters.
				for _, b := range body[before:decoder.InputOffset()] {
					if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
						return ErrInvalidFeed
					}
				}
			}
		case xml.Directive:
			return ErrInvalidFeed
		case xml.ProcInst:
			if value.Target != "xml" || roots != 0 || declaration || count != 0 || len(value.Inst) > 256 || !declarationPattern.Match(value.Inst) {
				return ErrInvalidFeed
			}
			declaration = true
		}
	}
	if roots != 1 || depth != 0 {
		return ErrInvalidFeed
	}
	return nil
}
