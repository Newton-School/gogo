package views

import (
	"errors"
	"net/http"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/security"
)

type LogoutConfig struct {
	CSRF       security.CSRFConfig
	SuccessURL string
}

// Logout accepts POST only. The caller's session is flushed and its CSRF
// secret rotated. It never accepts an untrusted redirect parameter.
func Logout(config LogoutConfig) (http.Handler, error) {
	if config.SuccessURL == "" {
		config.SuccessURL = "/"
	}
	if security.SafeNext(config.SuccessURL, "") == "" {
		return nil, errors.New("auth views: local success URL required")
	}
	csrf, err := accountCSRF(config.CSRF)
	if err != nil {
		return nil, err
	}
	protect, err := security.CSRF(csrf)
	if err != nil {
		return nil, err
	}
	return private(protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := auth.Logout(w, r, csrf); err != nil {
			unavailable(w)
			return
		}
		http.Redirect(w, r, config.SuccessURL, http.StatusSeeOther)
	}))), nil
}
