package examples_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/sessions"
)

func Example_session() {
	// A real HTTP request receives this context from sessions.Middleware.
	ctx := sessions.WithSession(context.Background(), sessions.New(sessions.Record{}))
	session, ok := sessions.FromContext(ctx)
	if !ok {
		panic("session middleware is required")
	}
	if err := session.Set("theme", "dark"); err != nil {
		panic(err)
	}
	var theme string
	found, err := session.Get("theme", &theme)
	if err != nil {
		panic(err)
	}
	fmt.Println(found, theme, session.Modified())
	// This tests context/state only. Middleware plus a real Store owns persistence.
	// Output:
	// true dark true
}
