package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
	"github.com/Newton-School/gogo/core/templates"
)

var errAccountMutationUnknown = errors.New("admin: account mutation outcome unknown")

// Account callbacks receive credentials. Never let a panic value reach the
// HTTP server logger; Atomic still owns rollback before this outer boundary.
func invokeAccountMutation(fn func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errAccountMutationUnknown
		}
	}()
	return fn()
}

func credentialForm(ctx context.Context, data url.Values, create, allowUnusable bool) (*forms.Form, error) {
	preserve := false
	fields := []forms.Field{}
	if create {
		fields = append(fields, forms.Field{Name: "identifier", Kind: forms.Char, Label: "Identifier", Required: true, MaxLength: 255, Widget: forms.InputWidget{Attrs: map[string]string{"autocomplete": "username"}}})
	}
	if !create || allowUnusable {
		fields = append(fields, forms.Field{Name: "password_mode", Kind: forms.ChoiceKind, Label: "Password access", Required: true, Initial: "set", Choices: []forms.Choice{{Value: "set", Label: "Set a new password"}, {Value: "unusable", Label: "Disable password authentication"}}})
	}
	required := create && !allowUnusable
	for _, field := range []struct{ name, label string }{{"password1", "Password"}, {"password2", "Confirm password"}} {
		fields = append(fields, forms.Field{Name: field.name, Kind: forms.Char, Label: field.label, Required: required, Strip: &preserve, MaxLength: 4096, Widget: forms.InputWidget{Type: "password", Attrs: map[string]string{"autocomplete": "new-password"}}})
	}
	opts := []forms.Option{forms.WithContext(ctx), forms.WithClean(func(form *forms.Form) error {
		setting := required || form.Value("password_mode") == "set"
		if setting && (form.Value("password1") == "" || form.Value("password1") != form.Value("password2")) {
			return forms.Error{Code: "password_mismatch", Message: "Enter matching passwords."}
		}
		return nil
	})}
	if data != nil {
		opts = append(opts, forms.WithData(data))
	}
	return forms.New(fields, opts...)
}

func (s *Site) credentialPost(w http.ResponseWriter, r *http.Request, create, allowUnusable bool) error {
	if err := s.parsePost(w, r); err != nil {
		return err
	}
	fields := []string{"_edit_token", "password1", "password2"}
	if create {
		fields = append(fields, "identifier")
	}
	if !create || allowUnusable {
		fields = append(fields, "password_mode")
	}
	for _, name := range fields {
		if len(r.PostForm[name]) != 1 {
			return errors.New("admin: invalid credential form")
		}
	}
	return nil
}

func (s *Site) userCreateForm(w http.ResponseWriter, r *http.Request, p auth.Principal, options ModelAdmin, store ScopedStore) {
	editor, ok := store.(UserEditor)
	if !ok {
		s.failure(w, r, errors.New("user editor unavailable"))
		return
	}
	unusableCreator, allowUnusable := store.(UserWithoutPasswordCreator)
	object := Object{}
	var data url.Values
	if r.Method == http.MethodPost {
		if err := s.credentialPost(w, r, true, allowUnusable); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		data = r.PostForm
	}
	form, err := credentialForm(r.Context(), data, true, allowUnusable)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	if data != nil {
		err = invokeAccountMutation(func() error {
			return store.Atomic(r.Context(), func(ctx context.Context) error {
				if err := s.allowed(ctx, p, "add", options, Object{}); err != nil {
					return err
				}
				if err := s.checkToken(data.Get("_edit_token"), p, options, Object{}, "add_user"); err != nil {
					return err
				}
				if !form.IsValid() {
					return errInvalidForm
				}
				if allowUnusable && form.Value("password_mode") == "unusable" {
					object, err = unusableCreator.CreateUserWithoutPassword(ctx, form.Value("identifier").(string), auth.CreateUserOptions{})
				} else {
					object, err = editor.CreateUser(ctx, form.Value("identifier").(string), form.Value("password1").(string), auth.CreateUserOptions{})
				}
				if err != nil {
					return err
				}
				if err := s.allowed(ctx, p, "add", options, object); err != nil {
					return err
				}
				after, err := snapshot(options, object)
				if err != nil {
					return err
				}
				return store.Audit(ctx, LogEntry{ActorID: p.ID, Site: s.config.Name, Model: options.Schema.Key(), ObjectID: object.ID, ObjectLabel: object.Label, Action: "add", Changes: diff(nil, after), At: time.Now().UTC()})
			})
		})
		if err == nil {
			s.successMessage(r, "The account was created successfully.")
			target := s.config.Prefix
			if s.allowed(r.Context(), p, "change", options, object) == nil {
				target = s.modelURL(options) + url.PathEscape(object.ID) + "/change/"
			} else if s.allowed(r.Context(), p, "view", options, Object{}) == nil {
				target = s.modelURL(options)
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		if errors.Is(err, auth.ErrPasswordValidation) {
			form.AddError("password1", forms.Error{Code: "password_policy", Message: "The password does not satisfy the account password policy."})
		} else if errors.Is(err, ErrAccountIdentifier) {
			form.AddError("identifier", forms.Error{Code: "invalid", Message: "Enter a valid account identifier."})
		} else if db.IsCode(err, db.UniqueViolation) {
			form.AddError("identifier", forms.Error{Code: "duplicate", Message: "An account with this identifier cannot be created."})
		} else if !errors.Is(err, errInvalidForm) {
			if accountOutcome(err) != auth.PasswordUnchanged {
				http.Error(w, "Account creation outcome requires review. Do not repeat automatically.", http.StatusServiceUnavailable)
			} else {
				s.failure(w, r, err)
			}
			return
		}
	}
	s.renderCredentialForm(w, r, p, options, Object{}, form, "add_user", "Add user", "Create account", "The new account starts active, without staff, superuser, group or permission grants. Account authority must explicitly approve creation.", s.modelURL(options))
}

func (s *Site) userPasswordForm(w http.ResponseWriter, r *http.Request, p auth.Principal, options ModelAdmin, store ScopedStore, id string) {
	editor, ok := store.(UserEditor)
	if !ok {
		s.failure(w, r, errors.New("user editor unavailable"))
		return
	}
	object, err := store.Get(r.Context(), id, false)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	if err := s.allowed(r.Context(), p, "change", options, object); err != nil {
		s.failure(w, r, err)
		return
	}
	targetID, err := object.Record.Get("id")
	if err != nil {
		s.failure(w, r, err)
		return
	}
	selfTarget := targetID == p.ID
	var data url.Values
	if r.Method == http.MethodPost {
		w.Header().Set("X-Gogo-Password-Change", string(auth.PasswordUnchanged))
		if err := s.credentialPost(w, r, false, true); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		data = r.PostForm
	}
	form, err := credentialForm(r.Context(), data, false, true)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	if data != nil {
		err = invokeAccountMutation(func() error {
			return store.Atomic(r.Context(), func(ctx context.Context) error {
				object, err = store.Get(ctx, id, true)
				if err != nil {
					return err
				}
				if err := s.allowed(ctx, p, "change", options, object); err != nil {
					return err
				}
				if err := s.checkToken(data.Get("_edit_token"), p, options, object, "password"); err != nil {
					return err
				}
				if !form.IsValid() {
					return errInvalidForm
				}
				if form.Value("password_mode") == "unusable" {
					object, err = editor.SetUserUnusablePassword(ctx, object)
				} else {
					object, err = editor.ChangeUserPassword(ctx, object, form.Value("password1").(string))
				}
				if err != nil {
					return err
				}
				if err := s.allowed(ctx, p, "change", options, object); err != nil {
					return err
				}
				return store.Audit(ctx, LogEntry{ActorID: p.ID, Site: s.config.Name, Model: options.Schema.Key(), ObjectID: object.ID, ObjectLabel: object.Label, Action: "change_password", Changes: map[string]Change{"password": {Before: "[redacted]", After: "[redacted]"}}, At: time.Now().UTC()})
			})
		})
		state := accountOutcome(err)
		w.Header().Set("X-Gogo-Password-Change", string(state))
		if state != auth.PasswordUnchanged {
			var logoutErr error
			if selfTarget {
				if _, present := sessions.FromContext(r.Context()); present {
					logoutErr = auth.Logout(w, r, s.config.CSRF)
				} else {
					logoutErr = security.RotateCSRFRequest(w, r, s.config.CSRF)
				}
				*r = *r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{}))
			}
			if err != nil || logoutErr != nil {
				http.Error(w, "Password change outcome requires a fresh login or account review. Do not repeat automatically.", http.StatusServiceUnavailable)
				return
			}
			s.successMessage(r, "The account password settings were changed successfully.")
			target := s.modelURL(options) + url.PathEscape(id) + "/change/"
			if selfTarget {
				target = s.config.LoginURL
				if target == "" {
					target = s.config.Prefix
				}
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		if errors.Is(err, auth.ErrPasswordValidation) {
			form.AddError("password1", forms.Error{Code: "password_policy", Message: "The password does not satisfy the account password policy."})
		} else if !errors.Is(err, errInvalidForm) {
			s.failure(w, r, err)
			return
		}
	}
	s.renderCredentialForm(w, r, p, options, object, form, "password", "Change password: "+object.Label, "Save password settings", "This privileged action requires explicit account authority and invalidates previous sessions. Changing your own password here requires a fresh login.", s.modelURL(options)+url.PathEscape(id)+"/change/")
}

func accountOutcome(err error) auth.PasswordChangeState {
	if err == nil {
		return auth.PasswordChanged
	}
	var committed *db.CommittedCallbackError
	if errors.As(err, &committed) {
		return auth.PasswordChanged
	}
	if errors.Is(err, errAccountMutationUnknown) || db.IsCode(err, db.UnknownCommit) {
		return auth.PasswordChangeUnknown
	}
	return auth.PasswordUnchanged
}

func (s *Site) renderCredentialForm(w http.ResponseWriter, r *http.Request, p auth.Principal, options ModelAdmin, object Object, form *forms.Form, action, title, label, instruction, back string) {
	body, err := form.Render("div")
	if err != nil {
		s.failure(w, r, err)
		return
	}
	token, err := s.token(p, options, object, action)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	status := http.StatusOK
	if form.IsBound() {
		status = http.StatusBadRequest
	}
	s.render(w, r, p, "user_credentials.html", templates.Context{"title": title, "form": body, "instruction": instruction, "submit_label": label, "edit_token": token, "back_url": back}, status)
}
