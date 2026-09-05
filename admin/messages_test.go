package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/messages"
)

func TestSharedMessagesFollowDurableSaveAndEscapeText(t *testing.T) {
	site, database := newTestSite(t)
	site.config.Messages = true
	flash, err := messages.Middleware(messages.Config{Mode: messages.Cookie, Signer: site.config.Signer})
	if err != nil {
		t.Fatal(err)
	}
	handler := flash(site)
	request := func(method, path string, data url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(data.Encode()))
		r = r.WithContext(auth.WithPrincipal(r.Context(), principal()))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "http://example.test")
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	get := request("GET", "/admin/shop/product/1/change/", nil, nil)
	values := url.Values{"Name": {"Saved"}, "_edit_token": {hidden(t, get.Body.String(), "_edit_token")}, "csrfmiddlewaretoken": {hidden(t, get.Body.String(), "csrfmiddlewaretoken")}}
	post := request("POST", "/admin/shop/product/1/change/", values, get.Result().Cookies())
	if post.Code != 303 {
		t.Fatal(post.Code, post.Body.String())
	}
	get = request("GET", "/admin/shop/product/", nil, post.Result().Cookies())
	if get.Code != 200 || !strings.Contains(get.Body.String(), "The record was saved successfully.") || !strings.Contains(get.Body.String(), `role="status"`) {
		t.Fatal(get.Code, get.Body.String())
	}
	for _, cookie := range get.Result().Cookies() {
		if cookie.Name == "gogo_messages" && cookie.MaxAge != -1 {
			t.Fatal("notice not consumed")
		}
	}
	data, _ := json.Marshal([]messages.Message{{Level: messages.Info, Text: `<script>alert(1)</script>`, Public: true}})
	token, err := site.config.Signer.Sign(data)
	if err != nil {
		t.Fatal(err)
	}
	get = request("GET", "/admin/", nil, []*http.Cookie{{Name: "gogo_messages", Value: token}})
	if strings.Contains(get.Body.String(), "<script>") || !strings.Contains(get.Body.String(), "&lt;script&gt;") {
		t.Fatal("message text not escaped", get.Body.String())
	}
	database.failAudit = true
	get = request("GET", "/admin/shop/product/1/change/", nil, nil)
	values.Set("Name", "Must roll back")
	values.Set("_edit_token", hidden(t, get.Body.String(), "_edit_token"))
	values.Set("csrfmiddlewaretoken", hidden(t, get.Body.String(), "csrfmiddlewaretoken"))
	post = request("POST", "/admin/shop/product/1/change/", values, get.Result().Cookies())
	if post.Code != 503 || database.records["1"].Name != "Saved" {
		t.Fatal("save should roll back", post.Code, database.records)
	}
	for _, cookie := range post.Result().Cookies() {
		if cookie.Name == "gogo_messages" && cookie.Value != "" {
			t.Fatal("failed commit emitted success")
		}
	}
}

func TestMessagesOptInRequiresMiddlewareBeforeWrites(t *testing.T) {
	site, database := newTestSite(t)
	site.config.Messages = true
	response := perform(site, "GET", "/admin/", principal(), nil, nil)
	if response.Code != 503 || len(database.logs) != 0 {
		t.Fatal(response.Code, database.logs)
	}
}
