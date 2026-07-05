package parity

import (
	"html"
	"regexp"
	"strings"
)

var (
	csrfNameValuePattern = regexp.MustCompile(`(?i)name="csrfmiddlewaretoken"\s+value="[^"]*"`)
	csrfValueNamePattern = regexp.MustCompile(`(?i)value="[^"]*"\s+name="csrfmiddlewaretoken"`)
	csrfMetaPattern      = regexp.MustCompile(`(?i)name="csrf-token" content="[^"]*"`)
	tokenPattern         = regexp.MustCompile(`(?i)(csrf-token|csrf_token|nonce|token)=["']?[A-Za-z0-9._:-]+["']?`)
	spacePattern         = regexp.MustCompile(`[ \t\n\r]+`)
	betweenTags          = regexp.MustCompile(`>\s+<`)
)

// NormalizeHTML removes volatile values and whitespace noise from admin HTML.
func NormalizeHTML(input string) string {
	normalized := strings.ReplaceAll(input, "\r\n", "\n")
	normalized = html.UnescapeString(normalized)
	normalized = csrfNameValuePattern.ReplaceAllString(normalized, `name="csrfmiddlewaretoken" value="<csrf>"`)
	normalized = csrfValueNamePattern.ReplaceAllString(normalized, `value="<csrf>" name="csrfmiddlewaretoken"`)
	normalized = csrfMetaPattern.ReplaceAllString(normalized, `name="csrf-token" content="<csrf>"`)
	normalized = tokenPattern.ReplaceAllString(normalized, `$1="<token>"`)
	normalized = betweenTags.ReplaceAllString(normalized, "><")
	normalized = spacePattern.ReplaceAllString(normalized, " ")
	return strings.TrimSpace(normalized)
}
