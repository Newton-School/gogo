package examples_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/Newton-School/gogo/core/urls"
)

func Example_routing() {
	// docs:begin route-handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, urls.Param(r, "id"))
	})
	// docs:end route-handler
	// docs:begin route-register
	router, err := urls.New(urls.Include("api/", "catalog",
		urls.Path("products/<int:id>/", handler, "detail", "GET"),
	))
	if err != nil {
		panic(err)
	}
	// docs:end route-register
	// docs:begin route-reverse
	path, err := router.Reverse("catalog:detail", map[string]any{"id": int64(42)}, nil)
	if err != nil {
		panic(err)
	}
	// docs:end route-reverse
	// docs:begin route-request
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
	fmt.Println(path)
	fmt.Println(response.Code, response.Body.String())
	// docs:end route-request
	// Output:
	// /api/products/42/
	// 200 42
}
