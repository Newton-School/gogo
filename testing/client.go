package testing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"reflect"
	"strings"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/sessions"
)

const (
	templateHeader   = "X-Gogo-Template-Used"
	formErrorsHeader = "X-Gogo-Form-Errors"
)

type Client struct {
	handler         http.Handler
	cookies         map[string]*http.Cookie
	session         *sessions.Session
	user            *auth.User
	followRedirects bool
}

type MultipartFile struct {
	Filename    string
	ContentType string
	Content     io.Reader
}

type Response struct {
	StatusCode    int
	Header        http.Header
	Body          string
	Request       *http.Request
	Cookies       []*http.Cookie
	RedirectChain []string
}

type TestHelper interface {
	Helper()
	Fatalf(string, ...any)
}

func NewClient(handler http.Handler) *Client {
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	return &Client{handler: handler, cookies: make(map[string]*http.Cookie)}
}

func (c *Client) WithSession(session *sessions.Session) *Client {
	c.session = session
	return c
}

func (c *Client) ForceLogin(user auth.User) *Client {
	user.Authenticated = true
	user.Anonymous = false
	c.user = &user
	return c
}

func (c *Client) FollowRedirects(enabled bool) *Client {
	c.followRedirects = enabled
	return c
}

func (c *Client) SetCookie(cookie *http.Cookie) *Client {
	if cookie != nil {
		c.cookies[cookie.Name] = cookie
	}
	return c
}

func (c *Client) Get(path string) *Response {
	return c.Do(http.MethodGet, path, nil, nil)
}

func (c *Client) Post(path string, body string) *Response {
	return c.Do(http.MethodPost, path, strings.NewReader(body), nil)
}

func (c *Client) Put(path string, body string) *Response {
	return c.Do(http.MethodPut, path, strings.NewReader(body), nil)
}

func (c *Client) Patch(path string, body string) *Response {
	return c.Do(http.MethodPatch, path, strings.NewReader(body), nil)
}

func (c *Client) Delete(path string) *Response {
	return c.Do(http.MethodDelete, path, nil, nil)
}

func (c *Client) Options(path string) *Response {
	return c.Do(http.MethodOptions, path, nil, nil)
}

func (c *Client) PostJSON(path string, payload any) *Response {
	return c.JSON(http.MethodPost, path, payload)
}

func (c *Client) PutJSON(path string, payload any) *Response {
	return c.JSON(http.MethodPut, path, payload)
}

func (c *Client) PatchJSON(path string, payload any) *Response {
	return c.JSON(http.MethodPatch, path, payload)
}

func (c *Client) JSON(method string, path string, payload any) *Response {
	body, err := json.Marshal(payload)
	if err != nil {
		return responseFromError(method, path, err)
	}
	return c.Do(method, path, bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
}

func (c *Client) PostForm(path string, values url.Values) *Response {
	return c.Do(http.MethodPost, path, strings.NewReader(values.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

func (c *Client) PostMultipart(path string, fields map[string]string, files map[string]MultipartFile) *Response {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return responseFromError(http.MethodPost, path, err)
		}
	}
	for name, file := range files {
		if file.Content == nil {
			file.Content = strings.NewReader("")
		}
		part, err := writer.CreatePart(multipartHeader(name, file))
		if err != nil {
			return responseFromError(http.MethodPost, path, err)
		}
		if _, err := io.Copy(part, file.Content); err != nil {
			return responseFromError(http.MethodPost, path, err)
		}
	}
	if err := writer.Close(); err != nil {
		return responseFromError(http.MethodPost, path, err)
	}
	return c.Do(http.MethodPost, path, body, map[string]string{"Content-Type": writer.FormDataContentType()})
}

func (c *Client) Do(method string, path string, body io.Reader, headers map[string]string) *Response {
	if c == nil {
		return responseFromError(method, path, fmt.Errorf("nil test client"))
	}
	currentMethod := method
	currentPath := path
	var chain []string
	for redirects := 0; redirects <= 10; redirects++ {
		response := c.perform(currentMethod, currentPath, body, headers)
		response.RedirectChain = append([]string(nil), chain...)
		if !c.followRedirects || !isRedirect(response.StatusCode) {
			return response
		}
		location := response.Header.Get("Location")
		if location == "" {
			return response
		}
		chain = append(chain, location)
		currentMethod = http.MethodGet
		currentPath = location
		body = nil
		headers = nil
	}
	return responseFromError(method, path, fmt.Errorf("too many redirects"))
}

func (c *Client) perform(method string, path string, body io.Reader, headers map[string]string) *Response {
	request := httptest.NewRequest(method, path, body)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	for _, cookie := range c.cookies {
		request.AddCookie(cookie)
	}
	if c.session != nil {
		request = request.WithContext(sessions.ContextWithSession(request.Context(), c.session))
	}
	if c.user != nil {
		request = request.WithContext(auth.ContextWithUser(request.Context(), *c.user))
	}

	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	result := recorder.Result()
	defer result.Body.Close()
	c.storeCookies(result.Cookies())
	bodyBytes, _ := io.ReadAll(result.Body)
	return &Response{
		StatusCode: result.StatusCode,
		Header:     result.Header.Clone(),
		Body:       string(bodyBytes),
		Request:    request,
		Cookies:    result.Cookies(),
	}
}

func (c *Client) storeCookies(cookies []*http.Cookie) {
	for _, cookie := range cookies {
		if cookie.MaxAge < 0 {
			delete(c.cookies, cookie.Name)
			continue
		}
		c.cookies[cookie.Name] = cookie
	}
}

func (r *Response) AssertStatus(t TestHelper, status int) {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("status = %d, want %d; body=%q", r.StatusCode, status, r.Body)
	}
}

func (r *Response) AssertHeader(t TestHelper, name string, value string) {
	t.Helper()
	if got := r.Header.Get(name); got != value {
		t.Fatalf("header %s = %q, want %q", name, got, value)
	}
}

func (r *Response) AssertBodyContains(t TestHelper, value string) {
	t.Helper()
	if !strings.Contains(r.Body, value) {
		t.Fatalf("body = %q, want it to contain %q", r.Body, value)
	}
}

func (r *Response) AssertJSONPath(t TestHelper, path string, want any) {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(r.Body), &decoded); err != nil {
		t.Fatalf("decode JSON response: %v; body=%q", err, r.Body)
	}
	got, ok := jsonPath(decoded, path)
	if !ok {
		t.Fatalf("JSON path %q missing in %s", path, r.Body)
	}
	if !reflect.DeepEqual(got, want) && fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("JSON path %q = %#v, want %#v", path, got, want)
	}
}

func (r *Response) AssertTemplateUsed(t TestHelper, name string) {
	t.Helper()
	for _, used := range r.Header.Values(templateHeader) {
		if used == name {
			return
		}
	}
	t.Fatalf("templates used = %#v, want %s", r.Header.Values(templateHeader), name)
}

func (r *Response) AssertRedirect(t TestHelper, target string) {
	t.Helper()
	if !isRedirect(r.StatusCode) {
		t.Fatalf("status = %d, want redirect to %s", r.StatusCode, target)
	}
	if got := r.Header.Get("Location"); got != target {
		t.Fatalf("redirect target = %q, want %q", got, target)
	}
}

func (r *Response) AssertFormError(t TestHelper, field string, message string) {
	t.Helper()
	errors := r.formErrors(t)
	for _, candidate := range errors[field] {
		if candidate == message {
			return
		}
	}
	t.Fatalf("form errors for %s = %#v, want %q", field, errors[field], message)
}

func (r *Response) AssertNonFieldError(t TestHelper, message string) {
	t.Helper()
	r.AssertFormError(t, "__all__", message)
}

func (r *Response) formErrors(t TestHelper) map[string][]string {
	t.Helper()
	var errors map[string][]string
	if err := json.Unmarshal([]byte(r.Header.Get(formErrorsHeader)), &errors); err != nil {
		t.Fatalf("decode form errors: %v", err)
	}
	return errors
}

func RecordTemplate(w http.ResponseWriter, name string) {
	w.Header().Add(templateHeader, name)
}

func RecordFormErrors(w http.ResponseWriter, errors map[string][]string) {
	body, err := json.Marshal(errors)
	if err != nil {
		return
	}
	w.Header().Set(formErrorsHeader, string(body))
}

func multipartHeader(name string, file MultipartFile) textproto.MIMEHeader {
	if file.Filename == "" {
		file.Filename = name
	}
	if file.ContentType == "" {
		file.ContentType = "application/octet-stream"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeMultipart(name), escapeMultipart(file.Filename)))
	header.Set("Content-Type", file.ContentType)
	return header
}

func escapeMultipart(value string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(value)
}

func responseFromError(method string, path string, err error) *Response {
	return &Response{
		StatusCode: http.StatusInternalServerError,
		Header:     make(http.Header),
		Body:       err.Error(),
		Request:    httptest.NewRequest(method, path, nil),
	}
}

func isRedirect(status int) bool {
	return status == http.StatusMovedPermanently ||
		status == http.StatusFound ||
		status == http.StatusSeeOther ||
		status == http.StatusTemporaryRedirect ||
		status == http.StatusPermanentRedirect
}

func jsonPath(value any, path string) (any, bool) {
	current := value
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
