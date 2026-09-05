package admin

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

type adminValidationChecker struct{ failure error }

func (c adminValidationChecker) ValidateUnique(context.Context, models.Record, []string) error {
	return c.failure
}
func (adminValidationChecker) ValidateConstraints(context.Context, models.Record, []string) error {
	return nil
}

func TestAdminModelValidationDistinguishesInputFailureFromProviderOutage(t *testing.T) {
	for _, mode := range []string{"success", "validation", "provider", "joined", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			site, database := newTestSite(t)
			validation := &models.ValidationError{}
			validation.Add("Name", "invalid", "Synthetic input rejection")
			provider := errors.New("synthetic private provider details")
			checker := adminValidationChecker{}
			switch mode {
			case "validation":
				checker.failure = validation
			case "provider":
				checker.failure = provider
			case "joined":
				checker.failure = errors.Join(validation, provider)
			case "cancel":
				checker.failure = errors.Join(validation, context.Canceled)
			}
			options := site.models["shop.Product"]
			options.ConstraintChecker = checker
			site.models["shop.Product"] = options
			path := "/admin/shop/product/1/change/"
			page := perform(site, "GET", path, principal(), nil, nil)
			if page.Code != 200 {
				t.Fatal(page.Code)
			}
			values := url.Values{"Name": {"Changed"}}
			for _, name := range []string{"csrfmiddlewaretoken", "_edit_token"} {
				m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(page.Body.String())
				if len(m) != 2 {
					t.Fatal("missing token")
				}
				values.Set(name, m[1])
			}
			response := perform(site, "POST", path, principal(), values, page.Result().Cookies())
			want := 503
			if mode == "validation" {
				want = 400
			}
			if mode == "success" {
				want = 303
			}
			if response.Code != want {
				t.Fatal("model validation status mismatch", response.Code, want)
			}
			if strings.Contains(response.Body.String(), provider.Error()) {
				t.Fatal("provider text leaked")
			}
			if mode != "success" && (database.records["1"].Name != "Public record" || len(database.logs) != 0) {
				t.Fatal("failed model validation wrote record/audit")
			}
			if mode != "validation" && strings.Contains(response.Body.String(), "Synthetic input rejection") {
				t.Fatal("mixed provider error exposed partial validation")
			}
		})
	}
}
