package admin

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/templates"
)

func TestSaveOnTopTemplateRepeatsControlsAroundFields(t *testing.T) {
	site, _ := newTestSite(t)
	body, err := site.engine.Render(context.Background(), "form.html", templates.Context{
		"save_on_top": true, "can_change": true, "can_add": true, "can_delete": true,
		"form": "FORM_CONTENT", "inlines": "INLINE_CONTENT", "delete_url": "/admin/shop/product/1/delete/",
		"csrf_token": "csrf", "edit_token": "edit", "relation_token": "relations", "account_grants_token": "grants",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(body, `name="_save"`) != 2 || strings.Index(body, `name="_save"`) > strings.Index(body, "FORM_CONTENT") || strings.LastIndex(body, `name="_save"`) < strings.Index(body, "INLINE_CONTENT") {
		t.Fatal("top and bottom controls do not surround the form and inlines", body)
	}
	for _, name := range []string{"csrfmiddlewaretoken", "_edit_token", "_relation_token", "_account_grants_token"} {
		if strings.Count(body, `name="`+name+`"`) != 1 {
			t.Fatal("duplicated hidden management field", name, body)
		}
	}
	rows := regexp.MustCompile(`<div class="submit-row">.*?</div>`).FindAllString(body, -1)
	if len(rows) != 2 || rows[0] != rows[1] {
		t.Fatal("submit rows use different controls", rows)
	}
}

func newSaveOnTopSite(t *testing.T, enabled bool, denied []string, readonly bool) (*Site, *testDB) {
	t.Helper()
	base, database := newTestSite(t)
	site, err := NewSite(base.config)
	if err != nil {
		t.Fatal(err)
	}
	options := base.models["shop.Product"]
	options.SaveOnTop = enabled
	options.Authorize = func(_ context.Context, _ auth.Principal, action string, _ Object) error {
		if slices.Contains(denied, action) {
			return auth.ErrPermissionDenied
		}
		return nil
	}
	if readonly {
		options.ReadonlyFields = []string{"Name", "Secret"}
	}
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	// Public registration owns a value snapshot, not this caller's descriptor.
	options.SaveOnTop = !enabled
	if site.models["shop.Product"].SaveOnTop != enabled {
		t.Fatal("registration did not snapshot SaveOnTop")
	}
	return site, database
}

func TestSaveOnTopUsesExistingFormPermissions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enabled       bool
		add, readonly bool
		denied        []string
		status, rows  int
		addButtons    int
		deleteLinks   int
	}{
		{name: "default-change", status: 200, rows: 1, addButtons: 1, deleteLinks: 1},
		{name: "default-add", add: true, status: 200, rows: 1, addButtons: 1},
		{name: "enabled-change", enabled: true, status: 200, rows: 2, addButtons: 2, deleteLinks: 2},
		{name: "enabled-add", enabled: true, add: true, status: 200, rows: 2, addButtons: 2},
		{name: "optional-actions-denied", enabled: true, denied: []string{"add", "delete"}, status: 200, rows: 2},
		{name: "view-only", enabled: true, denied: []string{"change"}, status: 200},
		{name: "add-denied-at-route", enabled: true, add: true, denied: []string{"add"}, status: 403},
		{name: "readonly-fields-retain-change-permission", enabled: true, readonly: true, status: 200, rows: 2, addButtons: 2, deleteLinks: 2},
		{name: "change-and-view-denied", enabled: true, denied: []string{"change", "view"}, status: 403},
		{name: "add-and-view-denied", enabled: true, add: true, denied: []string{"add", "view"}, status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site, _ := newSaveOnTopSite(t, tc.enabled, tc.denied, tc.readonly)
			path := "/admin/shop/product/1/change/"
			if tc.add {
				path = "/admin/shop/product/add/"
			}
			page := perform(site, "GET", path, principal(), nil, nil)
			body := page.Body.String()
			if page.Code != tc.status || strings.Count(body, `class="submit-row"`) != tc.rows || strings.Count(body, `name="_save"`) != tc.rows || strings.Count(body, `name="_continue"`) != tc.rows || strings.Count(body, `name="_addanother"`) != tc.addButtons || strings.Count(body, `class="deletelink"`) != tc.deleteLinks {
				t.Fatal("controls differ from existing permission decisions", page.Code, body)
			}
			if tc.status != 200 {
				return
			}
			if strings.Count(body, `<form `) != 1 || strings.Count(body, `</form>`) != 1 || strings.Count(body, `name="_edit_token"`) != 1 || strings.Count(body, `name="csrfmiddlewaretoken"`) != 1 {
				t.Fatal("extra form or duplicate management fields", body)
			}
			seen := map[string]bool{}
			for _, match := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(body, -1) {
				if seen[match[1]] {
					t.Fatal("duplicate DOM identity", match[1])
				}
				seen[match[1]] = true
			}
			if (tc.readonly || tc.rows == 0) && strings.Contains(body, `name="Name"`) {
				t.Fatal("readonly input became editable", body)
			}
			if tc.rows > 0 && !tc.readonly {
				field := strings.Index(body, `name="Name"`)
				if field < 0 || (strings.Index(body, `name="_save"`) < field) != tc.enabled || strings.LastIndex(body, `name="_save"`) < field {
					t.Fatal("submit row location did not match configuration", body)
				}
			}
		})
	}
}

func TestSaveOnTopPostFlagsKeepSingleSaveAndExistingRedirects(t *testing.T) {
	for _, add := range []bool{false, true} {
		for _, button := range []string{"_save", "_continue", "_addanother"} {
			site, database := newSaveOnTopSite(t, true, nil, false)
			path, id := "/admin/shop/product/1/change/", "1"
			if add {
				path, id = "/admin/shop/product/add/", "3"
			}
			get := perform(site, "GET", path, principal(), nil, nil)
			data := url.Values{"Name": {"Updated"}, button: {""}, "Secret": {"ignored"}, "csrfmiddlewaretoken": {hidden(t, get.Body.String(), "csrfmiddlewaretoken")}, "_edit_token": {hidden(t, get.Body.String(), "_edit_token")}}
			post := perform(site, "POST", path, principal(), data, get.Result().Cookies())
			target := "/admin/shop/product/"
			if button == "_continue" {
				target += id + "/change/"
			} else if button == "_addanother" {
				target += "add/"
			}
			if post.Code != 303 || post.Header().Get("Location") != target || database.records[id].Name != "Updated" || database.records[id].Secret == "ignored" || len(database.logs) != 1 {
				t.Fatal("duplicated save or changed submit behavior", add, button, post.Code, post.Header(), database.records, database.logs)
			}
		}
	}
}

func TestSaveOnTopInvalidAndDeniedSubmissionsDoNotWrite(t *testing.T) {
	for _, mode := range []string{"invalid-change", "invalid-add", "readonly-post", "denied-add-another", "missing-csrf", "stale-token"} {
		t.Run(mode, func(t *testing.T) {
			denied := []string{}
			if mode == "readonly-post" {
				denied = []string{"change"}
			} else if mode == "denied-add-another" {
				denied = []string{"add"}
			}
			site, database := newSaveOnTopSite(t, true, denied, false)
			path := "/admin/shop/product/1/change/"
			if mode == "invalid-add" {
				path = "/admin/shop/product/add/"
			}
			get := perform(site, "GET", path, principal(), nil, nil)
			data := url.Values{"Name": {"Updated"}, "_save": {""}, "csrfmiddlewaretoken": {hidden(t, get.Body.String(), "csrfmiddlewaretoken")}, "_edit_token": {hidden(t, get.Body.String(), "_edit_token")}}
			status := 403
			switch mode {
			case "invalid-change", "invalid-add":
				data.Set("Name", "")
				status = 400
			case "denied-add-another":
				data.Del("_save")
				data.Set("_addanother", "")
			case "missing-csrf":
				data.Del("csrfmiddlewaretoken")
			case "stale-token":
				data.Set("_edit_token", "not-a-signed-token")
				status = 409
			}
			post := perform(site, "POST", path, principal(), data, get.Result().Cookies())
			if post.Code != status || database.records["1"].Name != "Public record" || len(database.records) != 2 || len(database.logs) != 0 {
				t.Fatal("invalid or denied request wrote data", post.Code, post.Body.String(), database.records, database.logs)
			}
			if status == 400 {
				body := post.Body.String()
				if strings.Count(body, `name="_save"`) != 2 || strings.Count(body, `name="_edit_token"`) != 1 || strings.Count(body, `name="csrfmiddlewaretoken"`) != 1 || !strings.Contains(body, "This field is required.") || !strings.Contains(body, `aria-invalid="true"`) {
					t.Fatal("invalid rerender lost errors or repeated management fields", body)
				}
			}
		})
	}
}

func TestSaveOnTopSharedTemplateKeepsEscapingAndOverrideBoundary(t *testing.T) {
	site, _ := newTestSite(t)
	body, err := site.engine.Render(context.Background(), "form.html", templates.Context{"save_on_top": true, "can_change": true, "can_delete": true, "delete_url": `javascript:alert("unsafe")`})
	if err != nil || strings.Contains(body, "javascript:") || strings.Count(body, `href="#ZgotmplZ"`) != 2 {
		t.Fatal("shared delete links lost contextual escaping", body, err)
	}
	config := site.config
	config.TemplateLoaders = []templates.Loader{templates.MapLoader{"submit_row.html": `{% if can_change %}<div class="custom-submit"><button type="submit" name="_save">Save{% if can_add %} with add{% endif %}</button></div>{% endif %}{{ form }}{{ edit_token }}`}}
	custom, err := NewSite(config)
	if err != nil {
		t.Fatal(err)
	}
	body, err = custom.engine.Render(context.Background(), "form.html", templates.Context{"save_on_top": true, "can_change": true, "can_add": true, "form": "FORM_ONCE", "edit_token": "TOKEN_ONCE"})
	if err != nil || strings.Count(body, `class="custom-submit"`) != 2 || strings.Count(body, "Save with add") != 2 || strings.Count(body, "FORM_ONCE") != 1 || strings.Count(body, "TOKEN_ONCE") != 1 {
		t.Fatal("shared override did not receive only the explicit control context", body, err)
	}
}
