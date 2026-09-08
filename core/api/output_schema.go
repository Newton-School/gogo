package api

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/models"
)

// ErrOutputSchema means that output metadata is incomplete, invalid or exceeds
// the bounded schema projection contract. It never contains provider values.
var ErrOutputSchema = errors.New("api: output schema is unavailable")

const (
	outputSchemaDialect  = "https://json-schema.org/draft/2020-12/schema"
	outputSchemaMaxDepth = 32
	outputSchemaMaxNodes = 16384
	outputSchemaMaxBytes = 1 << 20
	outputSchemaMaxName  = 256
)

// WireSchema declares an application's custom JSON output contract. It is an
// assertion, not a runtime validator: serializers never execute it while
// representing data. Type must be any, null, boolean, integer, number, string,
// array or object. Explicit any permits all JSON values; missing metadata does
// not imply any. Arrays require Items. Objects are closed unless
// AdditionalProperties explicitly declares an additional-value schema.
//
// Format is an optional standard string-format annotation, not an assertion
// enforced by Gogo. Nullable admits null. Field output always additionally
// admits null because the serializer returns nil before its representation
// callback. MinItems/MaxItems apply only to arrays. References, extension
// keywords, executable values and implicit model fields are not supported.
// New copies this metadata under one aggregate 16,384-node/1-MiB text budget;
// later caller edits do not change the serializer.
type WireSchema struct {
	Type                 string
	Nullable             bool
	Format               string
	Properties           map[string]WireSchema
	Required             []string
	Items                *WireSchema
	AdditionalProperties *WireSchema
	MinItems, MaxItems   *int
}

// OutputSchemaOptions describes the surrounding resource's output policy.
type OutputSchemaOptions struct {
	// Redactable makes every top-level property optional, for example when a
	// Resource.AllowField policy can omit it. Nested required fields retain
	// their own serializer contract.
	Redactable bool
}

// OutputSchema returns deterministic JSON Schema 2020-12 for Representation,
// not input validation, routes, OpenAPI or generated clients. Only explicitly
// exposed fields are inspected. Schema generation invokes no model, field,
// representation or default callbacks and performs no I/O.
//
// Unknown/custom representations require Field.OutputSchema. Overrides are
// rejected for outputs whose structure is already derivable, rather than
// silently declaring stronger constraints than the serializer enforces. All
// field values are nullable; Model.Blank may also admit the literal empty
// string for non-string scalar fields. Date, time and duration outputs are
// strings without inferred format promises. JSON fields explicitly permit any
// JSON value. Policy-sensitive field omission requires Redactable.
//
// The schema is limited to depth 32, 16,384 nodes/field names/required names,
// 256-byte property names and 1 MiB of metadata text and encoded output. The
// bounded projection is detached before calling any context method. Returned
// bytes belong to the caller. Nil, panicking or invalid contexts fail closed.
func (s *Serializer) OutputSchema(ctx context.Context, options OutputSchemaOptions) (document []byte, err error) {
	defer func() {
		if recover() != nil {
			document, err = nil, ErrOutputSchema
		}
	}()
	budget := outputSchemaBudget{nodes: outputSchemaMaxNodes, text: outputSchemaMaxBytes}
	// Traversing only concrete metadata first freezes the operation against a
	// custom Context.Err that replaces a retained Serializer or nested handle.
	root, buildErr := budget.serializer(s, 0, options.Redactable)
	if err = outputSchemaContextError(ctx); err != nil {
		return nil, err
	}
	if buildErr != nil {
		return nil, buildErr
	}
	root.Dialect = outputSchemaDialect
	document, err = json.Marshal(root) // Private primitive-only representation.
	if err != nil || len(document) > outputSchemaMaxBytes {
		return nil, ErrOutputSchema
	}
	if err = outputSchemaContextError(ctx); err != nil {
		return nil, err
	}
	return document, nil
}

func outputSchemaContextError(ctx context.Context) error {
	if ctx == nil {
		return ErrOutputSchema
	}
	value := reflect.ValueOf(ctx)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return ErrOutputSchema
		}
	}
	switch err := ctx.Err(); err {
	case nil, context.Canceled, context.DeadlineExceeded:
		return err
	default:
		return ErrOutputSchema
	}
}

// All interface values below are created privately: Type is a string/string
// slice; AdditionalProperties is false or a node. No caller marshaler escapes.
type outputSchemaNode struct {
	Dialect              string                       `json:"$schema,omitempty"`
	Type                 any                          `json:"type,omitempty"`
	Format               string                       `json:"format,omitempty"`
	Properties           map[string]*outputSchemaNode `json:"properties,omitempty"`
	Required             []string                     `json:"required,omitempty"`
	Items                *outputSchemaNode            `json:"items,omitempty"`
	AdditionalProperties any                          `json:"additionalProperties,omitempty"`
	MinItems             *int                         `json:"minItems,omitempty"`
	MaxItems             *int                         `json:"maxItems,omitempty"`
	MinProperties        *int                         `json:"minProperties,omitempty"`
	MaxProperties        *int                         `json:"maxProperties,omitempty"`
	AnyOf                []*outputSchemaNode          `json:"anyOf,omitempty"`
	Const                *string                      `json:"const,omitempty"`
}

type outputSchemaBudget struct{ nodes, text int }

func (b *outputSchemaBudget) node(depth int) bool {
	if depth > outputSchemaMaxDepth || b.nodes <= 0 {
		return false
	}
	b.nodes--
	return true
}

func (b *outputSchemaBudget) name(name string) bool {
	if name == "" || len(name) > outputSchemaMaxName || len(name) > b.text || !utf8.ValidString(name) || strings.ContainsRune(name, 0) || !b.node(0) {
		return false
	}
	b.text -= len(name)
	return true
}

func (b *outputSchemaBudget) serializer(s *Serializer, depth int, redactable bool) (*outputSchemaNode, error) {
	if s == nil || !b.node(depth) || len(s.fields) > b.nodes {
		return nil, ErrOutputSchema
	}
	root := &outputSchemaNode{Type: "object", Properties: map[string]*outputSchemaNode{}, AdditionalProperties: false}
	for _, field := range s.fields {
		// Visibility itself is inspected work, even when no private metadata is
		// emitted. Repeated shared nested serializers must not multiply it.
		if !b.node(0) {
			return nil, ErrOutputSchema
		}
		if field.Hidden || field.WriteOnly {
			continue
		}
		if !b.name(field.Name) {
			return nil, ErrOutputSchema
		}
		if _, exists := root.Properties[field.Name]; exists {
			return nil, ErrOutputSchema
		}
		child, err := b.field(field, depth+1)
		if err != nil {
			return nil, err
		}
		root.Properties[field.Name] = child
		if field.Required && !redactable {
			if !b.name(field.Name) {
				return nil, ErrOutputSchema
			}
			root.Required = append(root.Required, field.Name)
		}
	}
	sort.Strings(root.Required)
	return root, nil
}

func (b *outputSchemaBudget) field(field Field, depth int) (*outputSchemaNode, error) {
	if !b.node(depth) {
		return nil, ErrOutputSchema
	}
	typeName, known := outputSchemaScalar(field.Model.Kind)
	ambiguous := field.Represent != nil || field.Nested == nil && field.Element == nil && (!known || field.Model.Codec != nil || field.Model.Relation != nil)
	var node *outputSchemaNode
	if ambiguous {
		if field.OutputSchema == nil {
			return nil, ErrOutputSchema
		}
		frozen, err := b.wire(*field.OutputSchema, depth)
		if err != nil {
			return nil, err
		}
		node = outputSchemaWireNode(frozen)
	} else {
		if field.OutputSchema != nil {
			return nil, ErrOutputSchema
		}
		switch {
		case field.Nested != nil:
			var err error
			node, err = b.serializer(field.Nested, depth, false)
			if err != nil {
				return nil, err
			}
		case field.Element != nil:
			if field.MinItems < 0 || field.MaxItems < 0 || field.MaxItems > 0 && field.MinItems > field.MaxItems {
				return nil, ErrOutputSchema
			}
			item, err := b.field(*field.Element, depth+1)
			if err != nil {
				return nil, err
			}
			if field.Dictionary {
				node = &outputSchemaNode{Type: "object", AdditionalProperties: item}
				if field.MinItems > 0 {
					node.MinProperties = outputSchemaInt(field.MinItems)
				}
				if field.MaxItems > 0 {
					node.MaxProperties = outputSchemaInt(field.MaxItems)
				}
			} else {
				node = &outputSchemaNode{Type: "array", Items: item}
				if field.MinItems > 0 {
					node.MinItems = outputSchemaInt(field.MinItems)
				}
				if field.MaxItems > 0 {
					node.MaxItems = outputSchemaInt(field.MaxItems)
				}
			}
		default:
			node = &outputSchemaNode{}
			if typeName != "any" {
				node.Type = typeName
			}
		}
	}
	outputSchemaNullable(node)
	if !ambiguous && field.Nested == nil && field.Element == nil && field.Model.Blank && typeName != "string" && typeName != "any" {
		if !b.node(depth) || !b.node(depth+1) {
			return nil, ErrOutputSchema
		}
		empty := ""
		node = &outputSchemaNode{AnyOf: []*outputSchemaNode{node, {Const: &empty}}}
	}
	return node, nil
}

func outputSchemaScalar(kind models.Kind) (string, bool) {
	switch kind {
	case models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger, models.SmallAuto, models.Auto, models.BigAuto:
		return "integer", true
	case models.Float:
		return "number", true
	case models.Boolean:
		return "boolean", true
	case models.Char, models.Text, models.Slug, models.Email, models.URL, models.GenericIPAddress, models.UUID, models.FilePath, models.File, models.Image, models.Decimal, models.Date, models.Time, models.DateTime, models.Duration, models.Binary:
		return "string", true
	case models.JSON:
		return "any", true
	default:
		return "", false
	}
}

func outputSchemaNullable(node *outputSchemaNode) {
	if value, ok := node.Type.(string); ok && value != "null" {
		node.Type = []string{value, "null"}
	}
}

func outputSchemaInt(value int) *int { return &value }

func freezeOutputSchema(schema *WireSchema, budget *outputSchemaBudget) (*WireSchema, error) {
	result, err := budget.wire(*schema, 0)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (b *outputSchemaBudget) wire(schema WireSchema, depth int) (WireSchema, error) {
	if !b.node(depth) || len(schema.Properties) > b.nodes || len(schema.Required) > b.nodes {
		return WireSchema{}, ErrOutputSchema
	}
	switch schema.Type {
	case "any", "null", "boolean", "integer", "number", "string", "array", "object":
	default:
		return WireSchema{}, ErrOutputSchema
	}
	if schema.Type != "object" && (schema.Properties != nil || len(schema.Required) != 0 || schema.AdditionalProperties != nil) || schema.Type != "array" && (schema.Items != nil || schema.MinItems != nil || schema.MaxItems != nil) {
		return WireSchema{}, ErrOutputSchema
	}
	if schema.Format != "" && (schema.Type != "string" || !outputSchemaFormat(schema.Format)) {
		return WireSchema{}, ErrOutputSchema
	}
	result := WireSchema{Type: schema.Type, Nullable: schema.Nullable, Format: schema.Format}
	if schema.Type == "object" {
		result.Properties = make(map[string]WireSchema, len(schema.Properties))
		for name, property := range schema.Properties {
			if !b.name(name) {
				return WireSchema{}, ErrOutputSchema
			}
			child, err := b.wire(property, depth+1)
			if err != nil {
				return WireSchema{}, err
			}
			result.Properties[name] = child
		}
		required := map[string]bool{}
		for _, name := range schema.Required {
			if !b.name(name) || required[name] {
				return WireSchema{}, ErrOutputSchema
			}
			if _, found := result.Properties[name]; !found {
				return WireSchema{}, ErrOutputSchema
			}
			required[name] = true
			result.Required = append(result.Required, name)
		}
		sort.Strings(result.Required)
		if schema.AdditionalProperties != nil {
			child, err := b.wire(*schema.AdditionalProperties, depth+1)
			if err != nil {
				return WireSchema{}, err
			}
			result.AdditionalProperties = &child
		}
	}
	if schema.Type == "array" {
		if schema.Items == nil || schema.MinItems != nil && *schema.MinItems < 0 || schema.MaxItems != nil && *schema.MaxItems < 0 || schema.MinItems != nil && schema.MaxItems != nil && *schema.MinItems > *schema.MaxItems {
			return WireSchema{}, ErrOutputSchema
		}
		child, err := b.wire(*schema.Items, depth+1)
		if err != nil {
			return WireSchema{}, err
		}
		result.Items = &child
		if schema.MinItems != nil {
			result.MinItems = outputSchemaInt(*schema.MinItems)
		}
		if schema.MaxItems != nil {
			result.MaxItems = outputSchemaInt(*schema.MaxItems)
		}
	}
	return result, nil
}

func outputSchemaWireNode(schema WireSchema) *outputSchemaNode {
	node := &outputSchemaNode{Format: schema.Format, MinItems: schema.MinItems, MaxItems: schema.MaxItems}
	if schema.Type != "any" {
		node.Type = schema.Type
	}
	if schema.Type == "object" {
		node.Properties = make(map[string]*outputSchemaNode, len(schema.Properties))
		for name, property := range schema.Properties {
			node.Properties[name] = outputSchemaWireNode(property)
		}
		node.Required = schema.Required
		node.AdditionalProperties = false
		if schema.AdditionalProperties != nil {
			node.AdditionalProperties = outputSchemaWireNode(*schema.AdditionalProperties)
		}
	}
	if schema.Items != nil {
		node.Items = outputSchemaWireNode(*schema.Items)
	}
	if schema.Nullable {
		outputSchemaNullable(node)
	}
	return node
}

func outputSchemaFormat(format string) bool {
	switch format {
	case "date-time", "date", "time", "duration", "email", "idn-email", "hostname", "idn-hostname", "ipv4", "ipv6", "uri", "uri-reference", "iri", "iri-reference", "uuid", "uri-template", "json-pointer", "relative-json-pointer", "regex":
		return true
	default:
		return false
	}
}
