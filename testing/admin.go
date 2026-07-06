package testing

import (
	"net/http"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/auth"
)

type AdminClient struct {
	*Client
}

func NewAdminClient(handler http.Handler) *AdminClient {
	return &AdminClient{Client: NewClient(handler)}
}

func (c *AdminClient) Login(user auth.User) *AdminClient {
	user.IsActive = true
	user.IsStaff = true
	user.Authenticated = true
	user.Anonymous = false
	c.ForceLogin(user)
	return c
}

func AssertAdminModelRegistered(t TestHelper, registry *admin.Registry, label string) {
	t.Helper()
	if registry == nil || !registry.IsRegistered(label) {
		t.Fatalf("admin model %s is not registered", label)
	}
}

func AssertAdminPage(t TestHelper, response *Response, text string) {
	t.Helper()
	response.AssertBodyContains(t, text)
}

func AssertAdminColumn(t TestHelper, response *Response, column string) {
	t.Helper()
	response.AssertBodyContains(t, column)
}
