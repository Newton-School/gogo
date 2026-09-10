package catalog

import (
	"net/http"

	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/urls"
)

func Routes() []urls.Route {
	return []urls.Route{
		urls.Path("catalog/", ghttp.Adapt(func(*http.Request) (ghttp.Response, error) {
			return ghttp.JSON(http.StatusOK, map[string]string{"app": "catalog"})
		}), "catalog-index", "GET"),
	}
}
