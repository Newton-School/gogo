package vector

import (
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/models"
	modelfields "github.com/Newton-School/gogo/models/fields"
)

// OpClass identifies a pgvector operator class.
type OpClass string

const (
	CosineOps OpClass = "vector_cosine_ops"
	L2Ops     OpClass = "vector_l2_ops"
	IPOps     OpClass = "vector_ip_ops"
)

// NewField creates a metadata field for PostgreSQL pgvector columns.
func NewField(options modelfields.Options, dimensions int) *modelfields.CustomField {
	return modelfields.NewCustomField(options, modelfields.CustomConfig{
		Kind:        "vector",
		ColumnTypes: map[string]string{"postgres": vectorKind(dimensions), "postgresql": vectorKind(dimensions)},
	})
}

// FieldMeta creates model metadata for a PostgreSQL pgvector column.
func FieldMeta(name, column string, dimensions int) models.FieldMeta {
	if dimensions < 1 || strings.TrimSpace(name) == "" {
		return models.FieldMeta{}
	}
	if strings.TrimSpace(column) == "" {
		column = name
	}
	return models.FieldMeta{Name: name, Column: column, Kind: vectorKind(dimensions)}
}

func vectorKind(dimensions int) string {
	if dimensions < 1 {
		return ""
	}
	return fmt.Sprintf("vector(%d)", dimensions)
}
