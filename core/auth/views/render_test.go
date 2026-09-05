package views

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountRenderPanicsDoNotReachHTTPLogger(t *testing.T) {
	for _, mode := range []string{"login", "change", "reset_request", "reset_confirm"} {
		t.Run(mode, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/form", nil)
			w := httptest.NewRecorder()
			switch mode {
			case "login":
				serveLogin(w, r, func(_ context.Context, page LoginPage) ([]byte, error) { panic(page.Identifier) }, LoginPage{Identifier: "private submitted identity"}, 200)
			case "change":
				servePasswordChange(w, r, func(_ context.Context, page PasswordChangePage) ([]byte, error) { panic(page.CSRFToken) }, PasswordChangePage{CSRFToken: "private form state"}, 200)
			case "reset_request":
				servePasswordResetRequest(w, r, func(context.Context, PasswordResetRequestPage) ([]byte, error) { panic("private template context") }, PasswordResetRequestPage{}, 200)
			case "reset_confirm":
				servePasswordResetConfirm(w, r, func(_ context.Context, page PasswordResetConfirmPage) ([]byte, error) { panic(page.Token.FormValue()) }, PasswordResetConfirmPage{Token: ResetFormToken{value: fixtureResetToken}}, 400)
			}
			if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), fixtureResetToken) {
				t.Fatal("renderer leaked private context", w.Code)
			}
		})
	}
}

func TestAccountRenderDropsPartialBytesAndCanceledOutput(t *testing.T) {
	for _, mode := range []string{"error", "cancel", "panic"} {
		ctx, cancel := context.WithCancel(context.Background())
		body, err := renderAccountPage(ctx, func(context.Context, string) ([]byte, error) {
			if mode == "cancel" {
				cancel()
				return []byte("private HTML"), nil
			}
			if mode == "panic" {
				panic("private panic")
			}
			return []byte("private HTML"), errors.New("private template error")
		}, "")
		cancel()
		if err == nil || body != nil || strings.Contains(err.Error(), "private") {
			t.Fatal("partial bytes or renderer error escaped", mode)
		}
	}
}
