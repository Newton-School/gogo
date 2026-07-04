package vector

import (
	"strings"

	"github.com/cybersaksham/gogo/models"
)

// HNSWIndex creates HNSW vector index metadata.
func HNSWIndex(name, field string, opClass OpClass) models.Index {
	return vectorIndex(name, field, "hnsw", opClass)
}

// IVFFlatIndex creates IVFFlat vector index metadata.
func IVFFlatIndex(name, field string, opClass OpClass) models.Index {
	return vectorIndex(name, field, "ivfflat", opClass)
}

func vectorIndex(name, field, method string, opClass OpClass) models.Index {
	if strings.TrimSpace(field) == "" {
		return models.Index{Name: name, Method: method}
	}
	opClassValue := strings.TrimSpace(string(opClass))
	index := models.Index{
		Name:   name,
		Fields: []models.IndexField{models.Asc(field).WithOpClass(opClassValue)},
		Method: method,
	}
	if opClassValue != "" {
		index.OpClasses = []string{opClassValue}
	}
	return index
}
