package urls

import "errors"

// ErrDescription indicates an invalid or over-budget route description request.
// No partial route inventory is returned on failure.
var ErrDescription = errors.New("URL description unavailable")

// Description is a detached inventory of the router's compiled route order.
// Routes have flattened prefixes/namespaces and no Children, just like Routes().
// Handler identities are retained; their internals remain application-owned.
type Description struct {
	Routes []Route
	// BuiltinConverters is true only for routers constructed by New. A router
	// constructed by NewWithConverters is false even when passed Builtins():
	// converter names and function values cannot prove trusted provenance.
	BuiltinConverters bool
}

// Describe returns bounded metadata without matching a path, reverse-encoding
// parameters, or invoking any handler or converter. maxRoutes must be 1..4096.
// The router must fit both maxRoutes and 16,384 total method entries; both
// limits are checked before copying routes or method slices. Route strings and
// handler identities are not interpreted. This does not authorize requests or
// assert that overlapping patterns are suitable for an API description.
func (r *Router) Describe(maxRoutes int) (Description, error) {
	if r == nil || maxRoutes < 1 || maxRoutes > 4096 {
		return Description{}, ErrDescription
	}
	operation := *r
	if len(operation.routes) > maxRoutes {
		return Description{}, ErrDescription
	}
	remainingMethods := 16384
	for _, route := range operation.routes {
		if len(route.route.Methods) > remainingMethods {
			return Description{}, ErrDescription
		}
		remainingMethods -= len(route.route.Methods)
	}
	return Description{Routes: operation.Routes(), BuiltinConverters: operation.builtinConverters}, nil
}
