package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/auth"
)

// SearchOptions controls admin search SQL generation.
type SearchOptions struct {
	Fields  []string
	Term    string
	Dialect string
}

// SearchQuery stores a generated search predicate.
type SearchQuery struct {
	Where             string
	Args              []any
	MayHaveDuplicates bool
}

// BuildSearchQuery builds a SQL predicate for Django-style admin search fields.
func BuildSearchQuery(options SearchOptions) (SearchQuery, error) {
	if strings.TrimSpace(options.Term) == "" || len(options.Fields) == 0 {
		return SearchQuery{}, nil
	}
	parts := make([]string, 0, len(options.Fields))
	args := make([]any, 0, len(options.Fields))
	result := SearchQuery{}
	for _, rawField := range options.Fields {
		lookup, field := parseSearchField(rawField)
		column := strings.ReplaceAll(field, "__", ".")
		if strings.Contains(field, "__") {
			result.MayHaveDuplicates = true
		}
		switch lookup {
		case "exact":
			parts = append(parts, column+" = ?")
			args = append(args, options.Term)
		case "startswith":
			parts = append(parts, "LOWER("+column+") LIKE LOWER(?)")
			args = append(args, options.Term+"%")
		case "fulltext":
			if options.Dialect == "postgres" || options.Dialect == "postgresql" {
				parts = append(parts, "to_tsvector("+column+") @@ plainto_tsquery(?)")
				args = append(args, options.Term)
			} else {
				parts = append(parts, "LOWER("+column+") LIKE LOWER(?)")
				args = append(args, "%"+options.Term+"%")
			}
		default:
			parts = append(parts, "LOWER("+column+") LIKE LOWER(?)")
			args = append(args, "%"+options.Term+"%")
		}
	}
	result.Where = "(" + strings.Join(parts, " OR ") + ")"
	result.Args = args
	return result, nil
}

func parseSearchField(raw string) (string, string) {
	if raw == "" {
		return "contains", raw
	}
	switch raw[0] {
	case '=':
		return "exact", raw[1:]
	case '^':
		return "startswith", raw[1:]
	case '@':
		return "fulltext", raw[1:]
	default:
		return "contains", raw
	}
}

// AutocompleteConfig configures the admin autocomplete endpoint.
type AutocompleteConfig struct {
	SearchFields         []string
	PageSize             int
	Rows                 []map[string]any
	ForwardedConstraints map[string]string
	HasPermission        func(*http.Request, auth.User) bool
}

// AutocompleteResult is one Select2-compatible result.
type AutocompleteResult struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// AutocompleteResponse is the autocomplete JSON payload.
type AutocompleteResponse struct {
	Results    []AutocompleteResult `json:"results"`
	Pagination struct {
		More bool `json:"more"`
	} `json:"pagination"`
}

// AutocompleteEndpoint returns a JSON autocomplete handler.
func AutocompleteEndpoint(config AutocompleteConfig) http.Handler {
	if config.PageSize <= 0 {
		config.PageSize = 20
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if config.HasPermission != nil && !config.HasPermission(r, user) {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		page, err := strconv.Atoi(valueOrDefault(r.URL.Query().Get("page"), "1"))
		if err != nil || page < 1 {
			http.Error(w, "invalid page", http.StatusBadRequest)
			return
		}
		matched := autocompleteRows(config, r)
		start := (page - 1) * config.PageSize
		end := start + config.PageSize
		response := AutocompleteResponse{}
		if start < len(matched) {
			if end > len(matched) {
				end = len(matched)
			}
			for _, row := range matched[start:end] {
				response.Results = append(response.Results, AutocompleteResult{ID: fmt.Sprint(row["id"]), Text: fmt.Sprint(row["title"])})
			}
		}
		response.Pagination.More = end < len(matched)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
}

func autocompleteRows(config AutocompleteConfig, r *http.Request) []map[string]any {
	term := strings.ToLower(r.URL.Query().Get("q"))
	matched := make([]map[string]any, 0)
	for _, row := range config.Rows {
		if !matchesForwardedConstraints(row, config.ForwardedConstraints, r.URL.Query()) {
			continue
		}
		if term != "" && !rowMatchesSearch(row, config.SearchFields, term) {
			continue
		}
		matched = append(matched, row)
	}
	return matched
}

func matchesForwardedConstraints(row map[string]any, constraints map[string]string, query map[string][]string) bool {
	for field, want := range constraints {
		forwarded := ""
		if values := query["forward_"+field]; len(values) > 0 {
			forwarded = values[0]
		}
		if forwarded != want || fmt.Sprint(row[field]) != want {
			return false
		}
	}
	return true
}

func rowMatchesSearch(row map[string]any, fields []string, term string) bool {
	for _, field := range fields {
		_, name := parseSearchField(field)
		if strings.Contains(strings.ToLower(fmt.Sprint(row[name])), term) {
			return true
		}
	}
	return false
}
