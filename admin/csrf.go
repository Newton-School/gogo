package admin

import (
	"net/http"

	gogohttp "github.com/Newton-School/gogo/http"
	"github.com/Newton-School/gogo/security"
)

const csrfFailureMessage = "CSRF verification failed"

func adminCSRFProtection(request *http.Request) *security.CSRFProtection {
	return security.NewCSRFProtection(security.CSRFOptions{
		CookieName:    gogohttp.CSRFCookieName,
		HeaderName:    gogohttp.CSRFHeaderName,
		FormFieldName: gogohttp.CSRFFormFieldName,
		SecureCookie:  isSecureRequest(request),
		SameSite:      http.SameSiteLaxMode,
		Path:          "/",
	})
}

func adminCSRFPageToken(request *http.Request) (string, *http.Cookie) {
	protector := adminCSRFProtection(request)
	token := ""
	if request != nil {
		if cookie, err := request.Cookie(protector.CookieName()); err == nil {
			token = cookie.Value
		}
	}
	if token == "" {
		token = protector.NewToken()
		return protector.Mask(token), &http.Cookie{
			Name:     protector.CookieName(),
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   isSecureRequest(request),
			SameSite: http.SameSiteLaxMode,
		}
	}
	return protector.Mask(token), nil
}

func validateAdminCSRF(request *http.Request) error {
	return adminCSRFProtection(request).Check(request)
}

func isSecureRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	return request.TLS != nil || request.URL.Scheme == "https"
}
