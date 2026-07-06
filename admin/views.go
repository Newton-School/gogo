package admin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/sessions"
)

// AuthViewConfig configures admin authentication views.
type AuthViewConfig struct {
	Site         *Site
	UserStore    auth.UserStore
	SessionStore sessions.Store
	Cookie       sessions.CookieOptions
}

// LoginView authenticates staff users and creates an admin session.
func LoginView(config AuthViewConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			renderAdminHTTPTemplate(w, "login.html", baseLoginPageData(config, r, ""))
			return
		}
		if err := validateAdminCSRF(r); err != nil {
			http.Error(w, csrfFailureMessage, http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		user, ok, err := auth.Authenticate(r.Context(), config.UserStore, auth.Credentials{
			Username: r.FormValue("username"),
			Email:    r.FormValue("email"),
			Password: r.FormValue("password"),
		})
		if err != nil {
			renderAdminHTTPTemplateStatus(w, "login.html", baseLoginPageData(config, r, "Please enter the correct username and password for a staff account."), http.StatusUnauthorized)
			return
		}
		if !ok || !user.IsStaff {
			renderAdminHTTPTemplateStatus(w, "login.html", baseLoginPageData(config, r, "Please enter the correct username and password for a staff account."), http.StatusForbidden)
			return
		}
		if err := saveAdminSession(r.Context(), w, config, user); err != nil {
			http.Error(w, "session failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, safeNextURL(config.site(), r.FormValue("next")), http.StatusFound)
	})
}

// LogoutView clears the admin session and redirects to login.
func LogoutView(config AuthViewConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			renderAdminHTTPTemplate(w, "logout.html", baseLogoutPageData(config, r))
			return
		}
		options := normalizeAdminCookie(config.Cookie)
		if config.SessionStore != nil {
			if cookie, err := r.Cookie(options.Name); err == nil && cookie.Value != "" {
				_ = config.SessionStore.Delete(r.Context(), cookie.Value)
			}
		}
		expireAdminCookie(w, options)
		http.Redirect(w, r, config.site().URLPrefix+"/login/", http.StatusFound)
	})
}

// PasswordChangeView changes the current staff user's password.
func PasswordChangeView(config AuthViewConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := currentAdminUser(r, config)
		if !ok || !user.IsStaff || !user.IsActive {
			http.Error(w, "admin access denied", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			renderAdminHTTPTemplate(w, "password_change.html", basePasswordChangePageData(config, r, ""))
			return
		}
		if err := validateAdminCSRF(r); err != nil {
			http.Error(w, csrfFailureMessage, http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		form := auth.PasswordChangeForm{User: user, OldPassword: r.FormValue("old_password"), NewPassword: r.FormValue("new_password")}
		valid, err := form.Validate()
		if err != nil || !valid {
			renderAdminHTTPTemplate(w, "password_change.html", basePasswordChangePageData(config, r, "Please correct the error below."))
			return
		}
		if err := form.Save(); err != nil {
			http.Error(w, "password change failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, config.site().URLPrefix+"/password_change/done/", http.StatusFound)
	})
}

func currentAdminUser(r *http.Request, config AuthViewConfig) (auth.User, bool) {
	if user, ok := auth.UserFromContext(r.Context()); ok && user.IsAuthenticated() && !user.IsAnonymous() {
		return user, true
	}
	return SessionPermissionPolicy{
		UserStore:    config.UserStore,
		SessionStore: config.SessionStore,
		Cookie:       config.Cookie,
	}.UserForRequest(r)
}

// PasswordChangeDoneView renders a small completion response.
func PasswordChangeDoneView() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><body>Password changed</body></html>"))
	})
}

func renderAdminHTTPTemplate(w http.ResponseWriter, name string, data adminPageData) {
	renderAdminHTTPTemplateStatus(w, name, data, http.StatusOK)
}

func renderAdminHTTPTemplateStatus(w http.ResponseWriter, name string, data adminPageData, status int) {
	site := adminSiteOrDefault(data.Site)
	data.Site = site
	rendered, err := RenderTemplate(name, data, site.TemplateDirs)
	if err != nil {
		http.Error(w, "admin template failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if data.csrfCookie != nil {
		http.SetCookie(w, data.csrfCookie)
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(rendered))
}

func baseLoginPageData(config AuthViewConfig, request *http.Request, errorMessage string) adminPageData {
	site := config.site()
	data := baseAdminPageData(site, request, "Log in", "Log in", "login")
	data.ContentTitle = ""
	data.Next = request.URL.Query().Get("next")
	if data.Next == "" {
		data.Next = request.FormValue("next")
	}
	data.Error = errorMessage
	data.Breadcrumbs = nil
	data.ShowNavSidebar = false
	return data
}

func baseLogoutPageData(config AuthViewConfig, request *http.Request) adminPageData {
	site := config.site()
	data := baseAdminPageData(site, request, "Logged out", "Logged out", "logout")
	data.UserName = ""
	data.Breadcrumbs = nil
	data.ShowNavSidebar = false
	return data
}

func basePasswordChangePageData(config AuthViewConfig, request *http.Request, errorMessage string) adminPageData {
	site := config.site()
	data := baseAdminPageData(site, request, "Password change", "Password change", "dashboard password-change")
	data.Error = errorMessage
	data.Breadcrumbs = append(data.Breadcrumbs, adminBreadcrumb{URL: site.URLPrefix + "/password_change/", Label: "Password change"})
	return data
}

func saveAdminSession(ctx context.Context, w http.ResponseWriter, config AuthViewConfig, user auth.User) error {
	if config.SessionStore == nil {
		return nil
	}
	options := normalizeAdminCookie(config.Cookie)
	session := sessions.NewSession(time.Duration(options.MaxAge) * time.Second)
	session.Set("user_id", strconv.FormatInt(user.ID, 10))
	if err := config.SessionStore.Save(ctx, session); err != nil {
		return err
	}
	cookie := &http.Cookie{
		Name:     options.Name,
		Value:    session.Key,
		Path:     options.Path,
		Domain:   options.Domain,
		HttpOnly: options.HttpOnly,
		Secure:   options.Secure,
		SameSite: options.SameSite,
		MaxAge:   options.MaxAge,
	}
	if !session.ExpireDate.IsZero() {
		cookie.Expires = session.ExpireDate
	}
	http.SetCookie(w, cookie)
	return nil
}

func expireAdminCookie(w http.ResponseWriter, options sessions.CookieOptions) {
	http.SetCookie(w, &http.Cookie{
		Name:     options.Name,
		Value:    "",
		Path:     options.Path,
		Domain:   options.Domain,
		HttpOnly: options.HttpOnly,
		Secure:   options.Secure,
		SameSite: options.SameSite,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
	})
}

func normalizeAdminCookie(options sessions.CookieOptions) sessions.CookieOptions {
	if options.Name == "" {
		options.Name = "gogo_sessionid"
	}
	if options.Path == "" {
		options.Path = "/"
	}
	if !options.HttpOnly {
		options.HttpOnly = true
	}
	if options.SameSite == 0 {
		options.SameSite = http.SameSiteLaxMode
	}
	if options.MaxAge == 0 {
		options.MaxAge = 12 * 60 * 60
	}
	return options
}

func (c AuthViewConfig) site() *Site {
	if c.Site != nil {
		return c.Site
	}
	return DefaultSite()
}

func safeNextURL(site *Site, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return site.URLPrefix + "/"
	}
	parsed, err := url.Parse(next)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(next, "//") || !strings.HasPrefix(next, "/") {
		return site.URLPrefix + "/"
	}
	return next
}
