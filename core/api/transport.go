package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	ghttp "github.com/Newton-School/gogo/core/http"
)

type ParseOptions struct {
	MaxBytes int64
	MaxDepth int
}
type Parsed struct {
	Values  Values
	cleanup func() error
}

func (p *Parsed) Close() error {
	if p.cleanup != nil {
		cleanup := p.cleanup
		p.cleanup = nil
		return cleanup()
	}
	return nil
}
func mediaError(status int, code, message string) error {
	return &ghttp.Error{Status: status, Code: code, Message: message}
}

// Parse accepts one bounded object. Multipart temporary files remain valid only
// until Parsed.Close; request handlers must defer Close after successful parsing.
func Parse(r *http.Request, options ParseOptions) (*Parsed, error) {
	if options.MaxBytes == 0 {
		options.MaxBytes = 10 << 20
	}
	if options.MaxDepth == 0 {
		options.MaxDepth = 32
	}
	if options.MaxBytes < 1 || options.MaxDepth < 1 || options.MaxDepth > 64 {
		return nil, errors.New("api: invalid parser limits")
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, mediaError(415, "UNSUPPORTED_MEDIA_TYPE", "Unsupported request content type")
	}
	if charset := strings.ToLower(params["charset"]); charset != "" && charset != "utf-8" {
		return nil, mediaError(415, "UNSUPPORTED_MEDIA_TYPE", "Only UTF-8 is supported")
	}
	if media != "application/json" && media != "application/x-www-form-urlencoded" && media != "multipart/form-data" {
		return nil, mediaError(415, "UNSUPPORTED_MEDIA_TYPE", "Unsupported request content type")
	}
	if r.ContentLength > options.MaxBytes {
		return nil, mediaError(413, "BODY_TOO_LARGE", "Request body too large")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, options.MaxBytes)
	bad := func(err error) error {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			return mediaError(413, "BODY_TOO_LARGE", "Request body too large")
		}
		return mediaError(400, "PARSE_ERROR", "Malformed request body")
	}
	if media == "multipart/form-data" {
		if err := r.ParseMultipartForm(min(options.MaxBytes, 2<<20)); err != nil {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
			return nil, bad(err)
		}
		values := formValues(r.MultipartForm.Value)
		if !validFormValues(values) {
			_ = r.MultipartForm.RemoveAll()
			return nil, bad(errors.New("UTF-8"))
		}
		for name, files := range r.MultipartForm.File {
			if !utf8.ValidString(name) {
				_ = r.MultipartForm.RemoveAll()
				return nil, bad(errors.New("UTF-8"))
			}
			for _, file := range files {
				if !utf8.ValidString(file.Filename) {
					_ = r.MultipartForm.RemoveAll()
					return nil, bad(errors.New("UTF-8"))
				}
			}
			if _, exists := values[name]; exists {
				_ = r.MultipartForm.RemoveAll()
				return nil, mediaError(400, "PARSE_ERROR", "Ambiguous multipart field")
			}
			if len(files) == 1 {
				values[name] = files[0]
			} else {
				items := make([]any, len(files))
				for i, f := range files {
					items[i] = f
				}
				values[name] = items
			}
		}
		return &Parsed{Values: values, cleanup: r.MultipartForm.RemoveAll}, nil
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, bad(err)
	}
	if !utf8.Valid(data) {
		return nil, bad(errors.New("UTF-8"))
	}
	if media == "application/x-www-form-urlencoded" {
		values, err := url.ParseQuery(string(data))
		if err != nil {
			return nil, bad(err)
		}
		parsed := formValues(values)
		if !validFormValues(parsed) {
			return nil, bad(errors.New("UTF-8"))
		}
		return &Parsed{Values: parsed}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeJSON(decoder, options.MaxDepth)
	if err != nil {
		return nil, bad(err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, bad(errors.New("trailing input"))
	}
	values, ok := object(value)
	if !ok {
		return nil, mediaError(400, "PARSE_ERROR", "Expected a JSON object")
	}
	return &Parsed{Values: values}, nil
}
func validFormValues(values Values) bool {
	for name, raw := range values {
		if !utf8.ValidString(name) {
			return false
		}
		switch value := raw.(type) {
		case string:
			if !utf8.ValidString(value) {
				return false
			}
		case []any:
			for _, item := range value {
				if !utf8.ValidString(item.(string)) {
					return false
				}
			}
		}
	}
	return true
}
func formValues(values url.Values) Values {
	out := Values{}
	for name, items := range values {
		if len(items) == 1 {
			out[name] = items[0]
		} else {
			list := make([]any, len(items))
			for i, v := range items {
				list[i] = v
			}
			out[name] = list
		}
	}
	return out
}

// Token decoding rejects duplicate keys, including escaped equivalents, instead
// of silently picking whichever actor/permission value appeared last.
func decodeJSON(d *json.Decoder, remaining int) (any, error) {
	if remaining < 0 {
		return nil, errors.New("depth")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			value := Values{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, errors.New("key")
				}
				if _, exists := value[name]; exists {
					return nil, errors.New("duplicate key")
				}
				child, err := decodeJSON(d, remaining-1)
				if err != nil {
					return nil, err
				}
				value[name] = child
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("object")
			}
			return value, nil
		case '[':
			value := []any{}
			for d.More() {
				child, err := decodeJSON(d, remaining-1)
				if err != nil {
					return nil, err
				}
				value = append(value, child)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("list")
			}
			return value, nil
		default:
			return nil, errors.New("delimiter")
		}
	}
	return token, nil
}

// Negotiate follows specificity before quality: application/json;q=0 cannot
// become acceptable merely because a less-specific */* has a positive quality.
func Negotiate(accept string, offered ...string) (string, error) {
	if accept == "" {
		accept = "*/*"
	}
	type preference struct {
		media       string
		params      map[string]string
		quality     float64
		specificity int
	}
	preferences := []preference{}
	parts, err := splitMediaRanges(accept)
	if err != nil {
		return "", mediaError(406, "NOT_ACCEPTABLE", "Malformed Accept header")
	}
	for _, part := range parts {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			return "", mediaError(406, "NOT_ACCEPTABLE", "Unsupported response representation")
		}
		quality := 1.0
		if text, ok := params["q"]; ok {
			quality, err = strconv.ParseFloat(text, 64)
			if err != nil || quality < 0 || quality > 1 {
				return "", mediaError(406, "NOT_ACCEPTABLE", "Invalid Accept quality")
			}
		}
		specificity := 2
		if media == "*/*" {
			specificity = 0
		} else if strings.HasSuffix(media, "/*") {
			specificity = 1
		}
		delete(params, "q")
		preferences = append(preferences, preference{media, params, quality, specificity})
	}
	best, bestQuality := "", 0.0
	for _, candidate := range offered {
		media, parameters, err := mime.ParseMediaType(candidate)
		if err != nil {
			return "", errors.New("api: invalid renderer media type")
		}
		quality, specificity, parameterCount := 0.0, -1, -1
		for _, p := range preferences {
			if p.media != media && p.media != "*/*" && p.media != strings.Split(media, "/")[0]+"/*" {
				continue
			}
			matches := true
			for key, value := range p.params {
				if parameters[key] != value {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			if p.specificity > specificity || p.specificity == specificity && (len(p.params) > parameterCount || len(p.params) == parameterCount && p.quality > quality) {
				quality, specificity, parameterCount = p.quality, p.specificity, len(p.params)
			}
		}
		if quality > bestQuality {
			best, bestQuality = candidate, quality
		}
	}
	if best == "" {
		return "", mediaError(406, "NOT_ACCEPTABLE", "Unsupported response representation")
	}
	return best, nil
}

func splitMediaRanges(value string) ([]string, error) {
	if len(value) > 8192 {
		return nil, errors.New("Accept limit")
	}
	parts := []string{}
	start := 0
	quoted, escaped := false, false
	for i, c := range value {
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if c == ',' && !quoted {
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil, errors.New("quoted media parameter")
	}
	parts = append(parts, value[start:])
	return parts, nil
}

// Adapt maps API field validation to the approved 422 contract while retaining
// the shared safe error envelope and aborted-transfer behavior.
func Adapt(view ghttp.View) http.Handler {
	return ghttp.Adapt(func(r *http.Request) (ghttp.Response, error) {
		response, err := view(r)
		var validation *ValidationError
		if errors.As(err, &validation) {
			fields := map[string][]string{}
			for name, items := range validation.Fields {
				for _, item := range items {
					fields[name] = append(fields[name], item.Message)
				}
			}
			return ghttp.Response{}, &ghttp.Error{Status: 422, Code: "VALIDATION_ERROR", Message: "Invalid input", Fields: fields}
		}
		return response, err
	})
}
