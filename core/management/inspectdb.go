package management

import (
	"context"
	"errors"
	"fmt"
	"go/token"
	"reflect"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// InspectDBOptions never adopts ownership. PrimaryKeys explicitly declares
// developer-verified non-null unique row identities for keyless relations (for
// example views); it cannot replace a database primary key or invent a column.
type InspectDBOptions struct {
	AppLabel, Package string
	Catalog           db.CatalogOptions
	PrimaryKeys       map[string][]string
}

// InspectDB returns complete unmanaged Go source, or no source on failure. It
// neither writes a file nor executes the returned metadata or model source.
func InspectDB(ctx context.Context, backend db.Backend, introspector db.CatalogIntrospector, options InspectDBOptions) ([]byte, error) {
	if inspectNil(ctx) || inspectNil(backend) || inspectNil(introspector) {
		return nil, errors.New("inspectdb: context, backend and catalog introspector required")
	}
	if err := validateInspectionOptions(options); err != nil {
		return nil, err
	}
	options = cloneInspectionOptions(options)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalogOptions := options.Catalog
	catalogOptions.Relations = slices.Clone(options.Catalog.Relations)
	catalog, err := introspector.InspectCatalog(ctx, backend, catalogOptions)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := RenderInspectedModels(catalog, options)
	if err != nil {
		return nil, &inspectionMappingError{err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return source, nil
}

type inspectionMappingError struct{ error }

func cloneInspectionOptions(options InspectDBOptions) InspectDBOptions {
	options.Catalog.Relations = slices.Clone(options.Catalog.Relations)
	if options.PrimaryKeys != nil {
		keys := make(map[string][]string, len(options.PrimaryKeys))
		for table, columns := range options.PrimaryKeys {
			keys[table] = slices.Clone(columns)
		}
		options.PrimaryKeys = keys
	}
	return options
}

func inspectNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}

func validateInspectionOptions(options InspectDBOptions) error {
	if !models.ValidIdentifier(options.AppLabel) || len(options.AppLabel) > 63 {
		return errors.New("inspectdb: a valid app label is required")
	}
	pkg := options.Package
	if pkg == "" {
		pkg = options.AppLabel
	}
	if !models.ValidIdentifier(pkg) || pkg == "_" || token.Lookup(pkg).IsKeyword() || len(pkg) > 63 {
		return errors.New("inspectdb: a valid Go package name is required")
	}
	if len(options.Catalog.Relations) > 1000 || len(options.PrimaryKeys) > 1000 {
		return errors.New("inspectdb: select at most 1000 relations")
	}
	for table, keys := range options.PrimaryKeys {
		if !models.ValidIdentifier(table) || len(keys) == 0 || len(keys) > 32 {
			return errors.New("inspectdb: explicit primary keys require a table and at most 32 existing columns")
		}
		seen := map[string]bool{}
		for _, key := range keys {
			if !models.ValidIdentifier(key) || seen[key] {
				return errors.New("inspectdb: invalid or duplicate explicit primary key column")
			}
			seen[key] = true
		}
	}
	return nil
}

type inspectedModel struct {
	schema  models.Schema
	catalog db.CatalogRelation
	fields  []inspectedField
}
type inspectedField struct {
	field  models.Field
	goType string
}

func inspectionError(table, column, message string) error {
	if column != "" {
		return fmt.Errorf("inspectdb: relation %q column %q: %s", table, column, message)
	}
	return fmt.Errorf("inspectdb: relation %q: %s", table, message)
}

// RenderInspectedModels validates catalog fidelity independently of connector
// diagnostics. Unsupported types and schema metadata are explicit errors. The
// output remains compatible with the normal gogo generate registration workflow.
func RenderInspectedModels(catalog db.Catalog, options InspectDBOptions) ([]byte, error) {
	if err := validateInspectionOptions(options); err != nil {
		return nil, err
	}
	if err := inspectionCatalogBounds(catalog); err != nil {
		return nil, err
	}
	if !models.ValidIdentifier(catalog.Schema) || len(catalog.Schema) > 63 || len(catalog.Relations) > 1000 {
		return nil, errors.New("inspectdb: invalid catalog schema or relation count")
	}
	if options.Catalog.Schema != "" && options.Catalog.Schema != catalog.Schema {
		return nil, errors.New("inspectdb: catalog schema differs from requested schema")
	}
	relations := slices.Clone(catalog.Relations)
	slices.SortFunc(relations, func(a, b db.CatalogRelation) int { return strings.Compare(a.Name, b.Name) })
	names := map[string]string{}
	usedNames := map[string]bool{}
	for _, relation := range relations {
		if !models.ValidIdentifier(relation.Name) || len(relation.Name) > 63 || names[relation.Name] != "" {
			return nil, inspectionError(relation.Name, "", "duplicate or unsupported table identifier")
		}
		name := inspectionGoName(relation.Name)
		if usedNames[name] || usedNames[name+"Fields"] {
			return nil, inspectionError(relation.Name, "", "model Go name collides with another model or generated field references")
		}
		usedNames[name], usedNames[name+"Fields"] = true, true
		names[relation.Name] = name
	}
	for table := range options.PrimaryKeys {
		if names[table] == "" {
			return nil, inspectionError(table, "", "explicit primary key refers to an unselected relation")
		}
	}
	if len(options.Catalog.Relations) > 0 {
		requested := map[string]bool{}
		for _, name := range options.Catalog.Relations {
			if requested[name] || names[name] == "" {
				return nil, inspectionError(name, "", "duplicate or missing selected relation")
			}
			requested[name] = true
		}
		if len(requested) != len(names) {
			return nil, errors.New("inspectdb: catalog contains unselected relations")
		}
	}
	var mapped []inspectedModel
	for _, relation := range relations {
		model, err := mapInspectedModel(catalog.Schema, relation, names, options)
		if err != nil {
			return nil, err
		}
		mapped = append(mapped, model)
	}
	registry := &models.Registry{}
	for _, model := range mapped {
		if err := registry.Register(model.schema); err != nil {
			return nil, err
		}
	}
	if err := registry.Freeze(); err != nil {
		return nil, fmt.Errorf("inspectdb: model relation metadata is unsupported: %w", err)
	}
	return inspectionSource(catalog.Schema, mapped, options)
}

func inspectionCatalogBounds(catalog db.Catalog) error {
	bytes, nodes := 0, 0
	var walk func(reflect.Value, int) error
	walk = func(v reflect.Value, depth int) error {
		nodes++
		if depth > 16 || nodes > 1_000_000 {
			return errors.New("inspectdb: catalog nesting or metadata count exceeds source limits")
		}
		switch v.Kind() {
		case reflect.String:
			if v.Len() > 64<<10 || bytes > (8<<20)-v.Len() {
				return errors.New("inspectdb: catalog text exceeds source limits")
			}
			bytes += v.Len()
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				return walk(v.Elem(), depth+1)
			}
		case reflect.Slice, reflect.Array:
			if v.Len() > 1600 {
				return errors.New("inspectdb: catalog list exceeds source limits")
			}
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if err := walk(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Func, reflect.Map, reflect.Chan:
			if !v.IsNil() {
				return errors.New("inspectdb: catalog contains custom runtime behavior")
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(catalog), 0)
}

func inspectionGoName(name string) string {
	var out strings.Builder
	for _, part := range strings.Split(name, "_") {
		if part == "" {
			continue
		}
		if strings.EqualFold(part, "id") {
			out.WriteString("ID")
		} else {
			out.WriteString(strings.ToUpper(part[:1]))
			out.WriteString(part[1:])
		}
	}
	result := out.String()
	if result == "" || result[0] < 'A' || result[0] > 'Z' {
		result = "Model" + result
	}
	return result
}
