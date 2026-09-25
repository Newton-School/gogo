package config

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Newton-School/gogo/async"
	"github.com/Newton-School/gogo/core/auth"
)

func TestDashboardRequiresActiveStaffSuperuser(t *testing.T) {
	for _, p := range []auth.Principal{{}, {Authenticated: true, Active: true}, {Authenticated: true, Active: true, Staff: true}, {Authenticated: true, Superuser: true, Staff: true}, {Authenticated: true, Active: true, Superuser: true}} {
		if dashboardAccess(auth.WithPrincipal(context.Background(), p)) != async.ErrDenied {
			t.Fatal("unauthorized inventory", p)
		}
	}
	p := auth.Principal{Authenticated: true, Active: true, Staff: true, Superuser: true}
	if err := dashboardAccess(auth.WithPrincipal(context.Background(), p)); err != nil {
		t.Fatal(err)
	}
	// Construction performs no Redis I/O. Anonymous access must fail before
	// any configured backend is inspected, including for static assets.
	h, err := (&Connections{}).Dashboard()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/async/", "/async/tasks", "/async/style.css"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 303 || w.Header().Get("Location") != "/admin/login/?next=/async/" {
			t.Fatal(path, w.Code)
		}
	}
}
