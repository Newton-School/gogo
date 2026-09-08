package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/auth"
)

// DownloadOptions configures a private attachment endpoint. Both callbacks are
// mandatory, read-only, cooperative, and receive fresh body-free request copies.
// ID selects a canonical metadata UUID, never an object key or filesystem path.
// Context values remain trusted application services, not deeply copied data.
type DownloadOptions struct {
	ID        func(*http.Request) (string, error)
	Authorize func(*http.Request) error
	Filename  string
	// Timeout defaults to 30 seconds; permitted values are 1ms through 5 minutes.
	// It cannot interrupt a blocking opaque callback, Reader, or ResponseWriter.
	Timeout time.Duration
}

type downloadHandler struct {
	service     Service
	id          func(*http.Request) (string, error)
	authorize   func(*http.Request) error
	disposition string
	timeout     time.Duration
}

// NewDownloadHandler explicitly mounts authorized GET/HEAD private downloads.
// Configure the existing core/http.ServerConfig.Streaming selector for this
// route and do not apply byte-changing compression or a buffering timeout layer.
func NewDownloadHandler(service *Service, options DownloadOptions) (http.Handler, error) {
	if service == nil || service.state == nil || options.ID == nil || options.Authorize == nil {
		return nil, ErrConfiguration
	}
	owned := *service
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.Timeout < time.Millisecond || options.Timeout > 5*time.Minute {
		return nil, ErrConfiguration
	}
	name := options.Filename
	if name == "" {
		name = "download"
	}
	if len(name) > 255 || !utf8.ValidString(name) || strings.Trim(name, ". ") == "" || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
		return nil, ErrConfiguration
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if disposition == "" {
		return nil, ErrConfiguration
	}
	return &downloadHandler{service: owned, id: options.ID, authorize: options.Authorize, disposition: disposition, timeout: options.Timeout}, nil
}

func (h *downloadHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	started := false
	var reader Reader
	closeReader := func() error {
		if missingValue(reader) {
			return nil
		}
		owned := reader
		reader = nil // ownership is released before any provider callback
		return safeServiceCall(owned.Close)
	}
	method := ""
	if req != nil {
		method = req.Method
	}
	defer func() {
		failure := recover()
		closeErr := closeReader()
		if failure != nil || closeErr != nil {
			if started {
				panic(http.ErrAbortHandler)
			}
			downloadStatus(w, method, http.StatusServiceUnavailable, &started)
		}
	}()
	if method != http.MethodGet && method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		downloadStatus(w, method, http.StatusMethodNotAllowed, &started)
		return
	}
	frozen, conditions, ok := freezeDownloadRequest(req)
	if !ok {
		downloadStatus(w, method, http.StatusBadRequest, &started)
		return
	}
	origin := detachedFileTime(time.Now())
	ctx := frozen.Context()
	if err := storageContext(ctx); err != nil {
		downloadStatus(w, method, http.StatusServiceUnavailable, &started)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	frozen = frozen.WithContext(ctx)
	check := func() error {
		if err := storageContext(ctx); err != nil {
			return err
		}
		err := h.authorize(downloadRequestCopy(frozen))
		if canceled := storageContext(ctx); canceled != nil {
			return canceled
		}
		return err
	}
	if err := check(); err != nil {
		downloadStatus(w, method, downloadErrorStatus(err), &started)
		return
	}
	id, err := h.id(downloadRequestCopy(frozen))
	if canceled := storageContext(ctx); canceled != nil {
		err = canceled
	}
	if err != nil {
		downloadStatus(w, method, downloadErrorStatus(err), &started)
		return
	}
	if canonical, err := metadataID(id); err != nil || canonical != id {
		downloadStatus(w, method, http.StatusNotFound, &started)
		return
	}
	// This final request grant precedes the service's current owner/link checks.
	if err := check(); err != nil {
		downloadStatus(w, method, downloadErrorStatus(err), &started)
		return
	}
	info, opened, err := h.service.Open(ctx, id)
	reader = opened
	if err != nil || missingValue(reader) {
		if err == nil {
			err = ErrUnavailable
		}
		if closeReader() != nil {
			err = ErrUnavailable
		}
		downloadStatus(w, method, downloadErrorStatus(err), &started)
		return
	}
	fail := func() {
		_ = closeReader()
		downloadStatus(w, method, http.StatusServiceUnavailable, &started)
	}
	if !downloadInfoValid(info, id) || storageContext(ctx) != nil {
		fail()
		return
	}
	size, err := reader.Seek(0, io.SeekEnd)
	if err != nil || size != info.Bytes || storageContext(ctx) != nil {
		fail()
		return
	}
	modified, strongDate := downloadModified(*info.FinalizedAt, origin)
	etag := downloadETag(info, h.disposition)
	status := downloadPrecondition(conditions, etag, modified)
	start, length := int64(0), size
	if status == 0 {
		status = http.StatusOK
		if method == http.MethodGet && (!conditions.ifRangePresent || conditions.ifRange != "" && downloadIfRange(conditions.ifRange, etag, modified, strongDate)) {
			if conditions.rangePresent && conditions.rangeValue == "" {
				status = http.StatusRequestedRangeNotSatisfiable
			} else {
				start, length, status = downloadRange(conditions.rangeValue, size)
			}
		}
	}
	body := method != http.MethodHead && (status == http.StatusOK || status == http.StatusPartialContent) && length > 0
	var first []byte
	if body {
		position, seekErr := reader.Seek(start, io.SeekStart)
		if seekErr != nil || position != start || storageContext(ctx) != nil {
			fail()
			return
		}
		first = make([]byte, min(int64(64<<10), length))
		n, readErr := downloadRead(ctx, reader, first, length)
		if readErr != nil {
			fail()
			return
		}
		first = first[:n]
	} else {
		if closeReader() != nil || storageContext(ctx) != nil {
			fail()
			return
		}
	}
	headers := http.Header{
		"Cache-Control": {"private, no-store"}, "X-Content-Type-Options": {"nosniff"},
		"Content-Type": {info.ContentType}, "Content-Disposition": {h.disposition},
		"Etag": {etag}, "Accept-Ranges": {"bytes"}, "Date": {origin.Format(http.TimeFormat)},
	}
	if !modified.IsZero() {
		headers.Set("Last-Modified", modified.Format(http.TimeFormat))
	}
	switch status {
	case http.StatusPartialContent:
		headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, size))
		headers.Set("Content-Length", strconv.FormatInt(length, 10))
	case http.StatusOK:
		headers.Set("Content-Length", strconv.FormatInt(size, 10))
	case http.StatusRequestedRangeNotSatisfiable:
		headers.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		headers.Set("Content-Length", "0")
	case http.StatusPreconditionFailed:
		headers.Set("Content-Length", "0")
	}
	if storageContext(ctx) != nil {
		fail()
		return
	}
	destination := w.Header()
	if storageContext(ctx) != nil {
		fail()
		return
	}
	// Remove stale framing/encoding supplied by outer middleware. This endpoint
	// emits exactly the authorized representation, never a compressed variant.
	for _, key := range []string{"Content-Length", "Content-Range", "Content-Encoding", "Transfer-Encoding", "Trailer", "Last-Modified"} {
		destination.Del(key)
	}
	for key, values := range headers {
		destination[key] = values
	}
	started = true
	w.WriteHeader(status)
	if storageContext(ctx) != nil {
		panic(http.ErrAbortHandler)
	}
	if !body {
		return
	}
	remaining := length
	buffer := first[:cap(first)]
	for {
		if err := downloadWrite(ctx, w, first); err != nil {
			panic(http.ErrAbortHandler)
		}
		remaining -= int64(len(first))
		if remaining == 0 {
			break
		}
		n, err := downloadRead(ctx, reader, buffer[:min(int64(len(buffer)), remaining)], remaining)
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		first = buffer[:n]
	}
	if closeReader() != nil || storageContext(ctx) != nil {
		panic(http.ErrAbortHandler)
	}
}

func downloadRead(ctx context.Context, reader Reader, buffer []byte, remaining int64) (int, error) {
	if err := storageContext(ctx); err != nil {
		return 0, err
	}
	n, err := reader.Read(buffer)
	if storageContext(ctx) != nil || n <= 0 || n > len(buffer) || int64(n) > remaining {
		return 0, ErrUnavailable
	}
	if err != nil && !(err == io.EOF && int64(n) == remaining) {
		return 0, ErrUnavailable
	}
	return n, nil
}

func downloadWrite(ctx context.Context, writer http.ResponseWriter, data []byte) error {
	if err := storageContext(ctx); err != nil {
		return err
	}
	n, err := writer.Write(data)
	if err != nil || n != len(data) || storageContext(ctx) != nil {
		return ErrUnavailable
	}
	return nil
}

func downloadInfoValid(info Info, id string) bool {
	if info.ID != id || info.State != Ready || info.Bytes < 0 || info.FinalizedAt == nil || info.FinalizedAt.IsZero() || len(info.Checksum) != 64 {
		return false
	}
	if _, err := hex.DecodeString(info.Checksum); err != nil {
		return false
	}
	kind, err := canonicalContentType(info.ContentType)
	return err == nil && kind == info.ContentType
}

func downloadETag(info Info, disposition string) string {
	value := info.ID + "\x00" + info.Checksum + "\x00" + strconv.FormatInt(info.Bytes, 10) + "\x00" + info.ContentType + "\x00" + disposition
	sum := sha256.Sum256([]byte(value))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func downloadErrorStatus(err error) int {
	switch err {
	case auth.ErrUnauthenticated:
		return http.StatusUnauthorized
	case auth.ErrPermissionDenied, ErrForbidden:
		return http.StatusForbidden
	case ErrInvalidOwner, ErrNotFound:
		return http.StatusNotFound
	default:
		return http.StatusServiceUnavailable
	}
}

func downloadStatus(w http.ResponseWriter, method string, status int, started *bool) {
	defer func() {
		if recover() != nil {
			panic(http.ErrAbortHandler)
		}
	}()
	if *started {
		panic(http.ErrAbortHandler)
	}
	header := w.Header()
	for _, key := range []string{"Etag", "Last-Modified", "Content-Range", "Content-Disposition", "Accept-Ranges", "Content-Encoding", "Transfer-Encoding", "Trailer"} {
		header.Del(key)
	}
	message := http.StatusText(status) + "\n"
	header.Set("Cache-Control", "private, no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Content-Length", strconv.Itoa(len(message)))
	*started = true
	w.WriteHeader(status)
	if method != http.MethodHead {
		if n, err := w.Write([]byte(message)); err != nil || n != len(message) {
			panic(http.ErrAbortHandler)
		}
	}
}

func downloadHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}

func freezeDownloadRequest(req *http.Request) (*http.Request, downloadConditions, bool) {
	var empty downloadConditions
	if req == nil || req.URL == nil || req.URL.User != nil || req.URL.Opaque != "" || req.URL.Fragment != "" || req.URL.RawFragment != "" {
		return nil, empty, false
	}
	// All scalar/container limits are checked before allocating copies or
	// invoking Context methods. Discarded body/form/TLS/transport state is never
	// traversed, cloned, read, closed, or passed to an application callback.
	for _, bound := range []struct {
		value string
		limit int
	}{{req.Method, 16}, {req.Host, 1024}, {req.RequestURI, 16 << 10}, {req.RemoteAddr, 1024}, {req.Proto, 64}} {
		if len(bound.value) > bound.limit || !utf8.ValidString(bound.value) || strings.ContainsFunc(bound.value, unicode.IsControl) {
			return nil, empty, false
		}
	}
	remaining := 16 << 10
	for _, value := range []string{req.URL.Scheme, req.URL.Host, req.URL.Path, req.URL.RawPath, req.URL.RawQuery} {
		if len(value) > remaining || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return nil, empty, false
		}
		remaining -= len(value)
	}
	if req.URL.Scheme != "" && req.URL.Scheme != "http" && req.URL.Scheme != "https" || !strings.HasPrefix(req.URL.Path, "/") {
		return nil, empty, false
	}
	if req.URL.RawPath != "" {
		path, err := url.PathUnescape(req.URL.RawPath)
		if err != nil || path != req.URL.Path {
			return nil, empty, false
		}
	}
	if len(req.Header) > 256 {
		return nil, empty, false
	}
	bytes, entries := 0, 0
	seen := make(map[string]bool, len(req.Header))
	for key, values := range req.Header {
		if len(key) > (64<<10)-bytes || !downloadHeaderName(key) || len(values) > 1024-entries {
			return nil, empty, false
		}
		bytes += len(key)
		entries += len(values)
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		if seen[canonical] {
			return nil, empty, false
		}
		seen[canonical] = true
		for _, value := range values {
			if len(value) > (64<<10)-bytes || strings.ContainsAny(value, "\r\n\x00") {
				return nil, empty, false
			}
			bytes += len(value)
		}
	}
	// Conditional shape validation precedes cloning even when the general header
	// budget is valid. Canonicalizing only keys here retains presence semantics.
	canonical := make(http.Header, len(req.Header))
	for key, values := range req.Header {
		canonical[textproto.CanonicalMIMEHeaderKey(key)] = values
	}
	if _, ok := downloadConditionShape(canonical); !ok {
		return nil, empty, false
	}
	header := canonical.Clone()
	conditions, _ := downloadConditionShape(header)
	urlCopy := *req.URL
	copy := &http.Request{
		Method: req.Method, URL: &urlCopy, Proto: req.Proto, ProtoMajor: req.ProtoMajor, ProtoMinor: req.ProtoMinor,
		Header: header, Body: http.NoBody, Host: req.Host, RemoteAddr: req.RemoteAddr, RequestURI: req.RequestURI,
	}
	return copy.WithContext(req.Context()), conditions, true
}

func downloadRequestCopy(req *http.Request) *http.Request {
	copy := *req
	urlCopy := *req.URL
	copy.URL = &urlCopy
	copy.Header = req.Header.Clone()
	return &copy
}
