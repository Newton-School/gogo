package models

import "context"

// ObjectQuery describes a bounded metadata-backed object query.
type ObjectQuery struct {
	Search       string
	SearchFields []string
	Filters      map[string][]string
	Ordering     []string
	Limit        int
	Offset       int
	IncludeTotal bool
}

// ObjectQueryResult stores rows and optional total count for an ObjectQuery.
type ObjectQueryResult struct {
	Rows  []map[string]any
	Total int
}

// ObjectQueryStore persists metadata rows and can execute bounded queries.
type ObjectQueryStore interface {
	Query(context.Context, Metadata, ObjectQuery) (ObjectQueryResult, error)
}
