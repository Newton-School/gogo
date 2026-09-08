package admin

import "errors"

// DocumentationPermission is the ordinary ModelPolicy grant checked by the
// documentation handler. Applications provision it explicitly; installing the
// optional documentation handler creates no model, content type or permission.
const DocumentationPermission = "admindocs.view_documentation"

var ErrDocumentation = errors.New("admin: documentation configuration unavailable")

// DocumentationField selects one declared field and optional plain-text help.
// Model defaults, database expressions, comments, codecs and validators are
// never examined or copied into documentation.
type DocumentationField struct {
	Name, Description string
}

// DocumentationModel selects a model already registered with this Site. Fields
// must be nonempty and explicit; nil never means every field. The site's view
// policy and the model's additional Authorize hook still narrow its visibility.
type DocumentationModel struct {
	Key, Description string
	Fields           []DocumentationField
}

type DocumentationRoute struct {
	Name, Pattern string
	Methods       []string
}

// DocumentationView is explicitly supplied prose, not reflected handler names
// or source-code documentation. Route names must occur in the route inventory.
type DocumentationView struct {
	Route, Title, Description string
}

type DocumentationExtension struct {
	Name, Description string
}

// DocumentationOptions is the concrete data-only input to the Site seam used
// by admin/admindocs. The Site snapshots and validates this input again, even
// when callers use the seam directly. It exposes only a fixed reference page,
// never an arbitrary template name, renderer callback or template context.
type DocumentationOptions struct {
	Models        []DocumentationModel
	Routes        []DocumentationRoute
	Views         []DocumentationView
	Tags, Filters []DocumentationExtension
}
