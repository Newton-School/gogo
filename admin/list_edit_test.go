package admin

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

func listTestSite(t *testing.T) (*Site, *testDB) {
	t.Helper()
	site, database := newTestSite(t)
	options := site.models["shop.Product"]
	options.ListEditable = []string{"Name"}
	site.models["shop.Product"] = options
	return site, database
}

func listTestPost(t *testing.T, body string) url.Values {
	t.Helper()
	return url.Values{"csrfmiddlewaretoken": {hidden(t, body, "csrfmiddlewaretoken")}, "_list_token": {hidden(t, body, "_list_token")}, "_save_list": {"1"}, "form-TOTAL_FORMS": {"1"}, "form-INITIAL_FORMS": {"1"}, "form-0-_id": {hidden(t, body, "form-0-_id")}, "form-0-Name": {"Updated record"}}
}

func TestListEditableRegistrationRejectsUnsupportedFields(t *testing.T) {
	site, _ := newTestSite(t)
	base := site.models["shop.Product"]
	base.ListEditable = []string{"Name"}
	if err := validateListEditable(base); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		alter func(*ModelAdmin)
	}{
		{"missing", func(o *ModelAdmin) { o.ListEditable = []string{"missing"} }},
		{"duplicate", func(o *ModelAdmin) { o.ListEditable = []string{"Name", "Name"} }},
		{"primary", func(o *ModelAdmin) { o.ListEditable = []string{"ID"} }},
		{"readonly", func(o *ModelAdmin) { o.ReadonlyFields = []string{"Name"} }},
		{"excluded", func(o *ModelAdmin) { o.Exclude = []string{"Name"} }},
		{"sensitive", func(o *ModelAdmin) { o.SensitiveFields = []string{"Name"} }},
		{"linked", func(o *ModelAdmin) { o.ListDisplayLinks = []string{"Name"} }},
		{"implicit link", func(o *ModelAdmin) { o.ListDisplay = []string{"Name", "ID"} }},
		{"undeclared", func(o *ModelAdmin) { o.Fields = []string{"Secret"} }},
		{"not displayed", func(o *ModelAdmin) { o.ListDisplay = []string{"ID"} }},
		{"custom display", func(o *ModelAdmin) { o.Columns = []DisplayColumn{{Name: "Name"}} }},
		{"account", func(o *ModelAdmin) { o.Schema.AppLabel = "gogo_auth" }},
		{"upload override", func(o *ModelAdmin) {
			o.FormOverrides = map[string]forms.Field{"Name": forms.NewField("Name", forms.File)}
		}},
		{"relation override", func(o *ModelAdmin) {
			o.FormOverrides = map[string]forms.Field{"Name": forms.NewField("Name", forms.ModelChoice)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.alter(&value)
			if validateListEditable(value) == nil {
				t.Fatal("accepted unsupported list edit configuration")
			}
		})
	}
}

func TestListEditableSnapshotsBeforeFutureRowCallbacks(t *testing.T) {
	site, database := listTestSite(t)
	scope := &testScope{db: database, tenant: "one", site: "admin"}
	first := scope.object(database.records["1"])
	second := scope.object(testRecord{ID: 3, Tenant: "one", Name: "Second", Secret: "server second"})
	field := forms.NewField("Name", forms.Char)
	field.Validators = []forms.Validator{func(_ context.Context, value any) error {
		if value == "Changed first" {
			return second.Record.Set("Secret", "changed by earlier validation")
		}
		return nil
	}}
	options := site.models["shop.Product"]
	options.FormOverrides = map[string]forms.Field{"Name": field}
	_, err := site.bindListEdit(context.Background(), principal(), options, []Object{first, second}, url.Values{"form-0-Name": {"Changed first"}, "form-1-Name": {"Changed second"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("future row mutation was accepted", err)
	}
}

func TestListEditableDeclarationSnapshot(t *testing.T) {
	base, _ := newTestSite(t)
	site, err := NewSite(base.config)
	if err != nil {
		t.Fatal(err)
	}
	options := base.models["shop.Product"]
	editable := []string{"Name"}
	options.ListEditable = editable
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	editable[0] = "Secret"
	if site.models["shop.Product"].ListEditable[0] != "Name" {
		t.Fatal("caller mutated registered allowlist")
	}
}

func TestListEditableSignedManifestAndAccessibleCells(t *testing.T) {
	site, database := listTestSite(t)
	get := perform(site, "GET", "/admin/shop/product/?q=Public", principal(), nil, nil)
	if get.Code != 200 {
		t.Fatal(get.Code, get.Body.String())
	}
	body := get.Body.String()
	for _, expected := range []string{`name="form-0-Name"`, `for="id_form-0-Name"`, `id="id_form-0-Name_help"`, `id="id_form-0-Name_errors"`, `name="_save_list"`, `name="form-0-_id"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	data := listTestPost(t, body)
	raw, err := site.config.Signer.Verify(data.Get("_list_token"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"Public", "keep-secret", "Updated record"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("manifest contains field/query values")
		}
	}
	data.Set("form-0-Secret", "forged")
	post := perform(site, "POST", "/admin/shop/product/?q=Public", principal(), data, get.Result().Cookies())
	if post.Code != 303 || post.Header().Get("X-Gogo-List-Change") != "changed" || post.Header().Get("Location") != "/admin/shop/product/?q=Public" {
		t.Fatal(post.Code, post.Header(), post.Body.String())
	}
	if database.records["1"].Name != "Updated record" || database.records["1"].Secret != "keep-secret" || len(database.logs) != 1 {
		t.Fatal("save or audit failed")
	}
}

func TestListEditableHiddenLabelsUseTheCellContainingBlock(t *testing.T) {
	site, _ := listTestSite(t)
	page := perform(site, "GET", "/admin/shop/product/", principal(), nil, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `<div class="list-edit-cell"><label class="visually-hidden"`) {
		t.Fatal("cell lost its accessible label", page.Code)
	}
	css := strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(string(site.css))
	// The table deliberately scrolls horizontally. Absolute off-screen labels
	// need a positioned cell ancestor, otherwise their containing block can be
	// the document and they expand mobile page width beyond the table clip.
	if !strings.Contains(css, `.list-edit-cell{position:relative;`) || !strings.Contains(css, `.table-scroll{overflow-x:auto;}`) {
		t.Fatal("hidden labels can escape the scrolling table's containing block")
	}
	if strings.Contains(css, "body{overflow") || strings.Contains(css, "html{overflow") {
		t.Fatal("page-level overflow masking is not a containing-block fix")
	}
}

func TestListEditableRejectsTamperedManagementAndStalePages(t *testing.T) {
	for _, test := range []struct {
		name   string
		alter  func(url.Values, *testDB)
		path   string
		status int
	}{
		{"signature", func(v url.Values, _ *testDB) { v.Set("_list_token", "invalid") }, "", 409},
		{"duplicate token", func(v url.Values, _ *testDB) { v.Add("_list_token", v.Get("_list_token")) }, "", 400},
		{"duplicate action", func(v url.Values, _ *testDB) { v["action"] = []string{"", "delete"} }, "", 400},
		{"action ambiguity", func(v url.Values, _ *testDB) { v.Set("action", "delete") }, "", 400},
		{"count", func(v url.Values, _ *testDB) { v.Set("form-TOTAL_FORMS", "1000000") }, "", 400},
		{"duplicate identity", func(v url.Values, _ *testDB) { v.Add("form-0-_id", "1") }, "", 400},
		{"foreign identity", func(v url.Values, _ *testDB) { v.Set("form-0-_id", "2") }, "", 400},
		{"missing identity", func(v url.Values, _ *testDB) { v.Del("form-0-_id") }, "", 400},
		{"extra identity", func(v url.Values, _ *testDB) { v.Set("form-1-_id", "2") }, "", 400},
		{"query binding", func(url.Values, *testDB) {}, "?o=Name", 409},
		{"stale", func(_ url.Values, d *testDB) {
			row := d.records["1"]
			row.Secret = "changed outside editor"
			d.records["1"] = row
		}, "", 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			site, database := listTestSite(t)
			get := perform(site, "GET", "/admin/shop/product/", principal(), nil, nil)
			data := listTestPost(t, get.Body.String())
			test.alter(data, database)
			post := perform(site, "POST", "/admin/shop/product/"+test.path, principal(), data, get.Result().Cookies())
			if post.Code != test.status || len(database.logs) != 0 || database.records["1"].Name != "Public record" {
				t.Fatal(post.Code, post.Body.String(), len(database.logs))
			}
		})
	}
}

func TestListEditableInvalidReadonlyAndOperationalFailures(t *testing.T) {
	for _, mode := range []string{"invalid", "readonly", "object denied", "model denied", "provider", "audit"} {
		t.Run(mode, func(t *testing.T) {
			site, database := listTestSite(t)
			get := perform(site, "GET", "/admin/shop/product/", principal(), nil, nil)
			data := listTestPost(t, get.Body.String())
			options := site.models["shop.Product"]
			status := 400
			switch mode {
			case "invalid":
				data.Set("form-0-Name", "")
			case "readonly":
				options.GetReadonlyFields = func(context.Context, Object) []string { return []string{"Name"} }
				status = 303
			case "object denied":
				options.Authorize = func(_ context.Context, _ auth.Principal, action string, object Object) error {
					if action == "change" && object.ID != "" {
						return auth.ErrPermissionDenied
					}
					return nil
				}
				status = 303
			case "model denied":
				options.Authorize = func(_ context.Context, _ auth.Principal, action string, object Object) error {
					if action == "change" && object.ID == "" {
						return auth.ErrPermissionDenied
					}
					return nil
				}
				status = 403
			case "provider":
				options.Authorize = func(context.Context, auth.Principal, string, Object) error { return errors.New("private outage") }
				status = 503
			case "audit":
				database.failAudit = true
				status = 503
			}
			site.models["shop.Product"] = options
			post := perform(site, "POST", "/admin/shop/product/", principal(), data, get.Result().Cookies())
			if post.Code != status || database.records["1"].Name != "Public record" || len(database.logs) != 0 || strings.Contains(post.Body.String(), "private outage") {
				t.Fatal(post.Code, post.Body.String(), len(database.logs))
			}
			if mode == "invalid" && (!strings.Contains(post.Body.String(), "No records were saved.") || !strings.Contains(post.Body.String(), `aria-invalid="true"`)) {
				t.Fatal("invalid cells not shown", post.Body.String())
			}
		})
	}
}

func TestListEditableModelValidationProviderAndPanicAreNotInputErrors(t *testing.T) {
	for _, mode := range []string{"validation", "provider", "joined", "cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			site, database := listTestSite(t)
			options := site.models["shop.Product"]
			validation := &models.ValidationError{}
			validation.Add("Name", "invalid", "Synthetic input rejection")
			provider := errors.New("private validation provider")
			failure := provider
			switch mode {
			case "validation":
				failure = validation
			case "joined":
				failure = errors.Join(validation, provider)
			case "cancel":
				failure = errors.Join(validation, context.Canceled)
			case "panic":
				field := forms.NewField("Name", forms.Char)
				field.Validators = []forms.Validator{func(context.Context, any) error { panic("private validation provider") }}
				options.FormOverrides = map[string]forms.Field{"Name": field}
			}
			options.ConstraintChecker = adminValidationChecker{failure: failure}
			site.models["shop.Product"] = options
			get := perform(site, "GET", "/admin/shop/product/", principal(), nil, nil)
			if get.Code != 200 {
				t.Fatal(get.Code, get.Body.String())
			}
			post := perform(site, "POST", "/admin/shop/product/", principal(), listTestPost(t, get.Body.String()), get.Result().Cookies())
			status := 503
			if mode == "validation" {
				status = 400
			}
			if post.Code != status || strings.Contains(post.Body.String(), provider.Error()) || database.records["1"].Name != "Public record" || len(database.logs) != 0 {
				t.Fatal(post.Code, post.Body.String())
			}
			if mode != "validation" && strings.Contains(post.Body.String(), "Synthetic input rejection") {
				t.Fatal("operational failure leaked validation fields")
			}
		})
	}
}
