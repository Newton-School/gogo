package templates

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrDescription refuses an invalid or over-budget extension inventory.
var ErrDescription = errors.New("templates: description unavailable")

// Description contains detached effective names, not executable extensions.
// Tags lists opening and standalone tags; closing tags and branch delimiters
// are syntax belonging to their enclosing tag, not independently callable tags.
// Names do not claim that a filter still has its builtin implementation: a
// configured filter with the same name replaces that implementation.
type Description struct {
	Tags, Filters []string
}

// These names include lexical tags, structural inheritance and rendering tags.
// Custom registrations cannot replace the corresponding parser/render cases.
var descriptionBuiltinTags = [...]string{
	"autoescape", "block", "comment", "csrf_token", "cycle", "debug", "extends",
	"filter", "firstof", "for", "get_current_timezone", "if", "ifchanged", "include",
	"load", "localtime", "now", "partialdef", "querystring", "regroup", "resetcycle",
	"spaceless", "templatetag", "timezone", "verbatim", "widthratio", "with",
}

// Describe inventories immutable configuration without loading, parsing or
// rendering any template and without invoking a tag, filter or processor.
// maxEntries must be 1..4096. The bound covers inspected registrations as well
// as builtin tag names, including registrations shadowed by builtin syntax.
func (e *Engine) Describe(maxEntries int) (Description, error) {
	if e == nil || maxEntries < 1 || maxEntries > 4096 {
		return Description{}, ErrDescription
	}
	config := e.config
	if config.Filters == nil || config.Tags == nil {
		return Description{}, ErrDescription
	}
	remaining := maxEntries - len(descriptionBuiltinTags)
	if remaining < 0 || len(config.Tags) > remaining {
		return Description{}, ErrDescription
	}
	remaining -= len(config.Tags)
	if len(config.Filters) > remaining {
		return Description{}, ErrDescription
	}
	seen := make(map[string]bool, len(descriptionBuiltinTags)+len(config.Tags))
	tags := make([]string, 0, len(descriptionBuiltinTags)+len(config.Tags))
	for _, name := range descriptionBuiltinTags {
		seen[name] = true
		tags = append(tags, name)
	}
	for name, fn := range config.Tags {
		if !descriptionName(name, false) {
			return Description{}, ErrDescription
		}
		// These internal node names and parser delimiters never dispatch a
		// configured tag. Do not advertise their unreachable callbacks.
		if seen[name] || name == "text" || name == "value" || name == "else" || name == "elif" || name == "empty" || strings.HasPrefix(name, "end") {
			continue
		}
		if fn == nil {
			return Description{}, ErrDescription
		}
		seen[name] = true
		tags = append(tags, name)
	}
	filters := make([]string, 0, len(config.Filters))
	for name, fn := range config.Filters {
		if !descriptionName(name, true) || fn == nil {
			return Description{}, ErrDescription
		}
		filters = append(filters, name)
	}
	slices.Sort(tags)
	slices.Sort(filters)
	return Description{Tags: tags, Filters: filters}, nil
}

func descriptionName(name string, filter bool) bool {
	if name == "" || len(name) > 128 || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00{}") || filter && strings.ContainsAny(name, "|:") {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
