package http

import "strings"

// matchesIfNoneMatch compares an already selected representation for GET/HEAD.
// RFC 9110 uses weak comparison and opaque entity-tag bytes, not quoted-string
// escaping. A match is used only after the entire bounded list is validated;
// malformed or excessive cache hints fall back to the full response.
func matchesIfNoneMatch(lines []string, current string) bool {
	if len(lines) == 0 || len(lines) > 32 {
		return false
	}
	// Count separators before allocating the combined field value.
	size := len(lines) - 1
	for _, line := range lines {
		if len(line) > 4096-size {
			return false
		}
		size += len(line)
	}
	value := strings.Trim(strings.Join(lines, ","), " \t")
	if value == "*" {
		return true
	}
	currentTag, rest, validCurrent := takeEntityTag(current)
	validCurrent = validCurrent && rest == ""
	matched := false
	elements := 0
	for value != "" {
		elements++
		if elements > 64 {
			return false
		}
		value = strings.TrimLeft(value, " \t")
		if value == "" {
			break
		}
		if value[0] == ',' {
			value = value[1:]
			continue
		}
		tag, remaining, valid := takeEntityTag(value)
		if !valid {
			return false
		}
		if validCurrent && tag == currentTag {
			matched = true
		}
		value = strings.TrimLeft(remaining, " \t")
		if value != "" {
			if value[0] != ',' {
				return false
			}
			value = value[1:]
		}
	}
	return matched
}

// takeEntityTag returns the quoted opaque tag without its optional weak prefix.
// The unconsumed suffix is checked by the caller for list or field boundaries.
func takeEntityTag(value string) (tag, rest string, ok bool) {
	value = strings.TrimPrefix(value, "W/")
	if len(value) == 0 || value[0] != '"' {
		return "", "", false
	}
	for i := 1; i < len(value); i++ {
		b := value[i]
		if b == '"' {
			return value[:i+1], value[i+1:], true
		}
		if b < 0x21 || b == 0x7f {
			return "", "", false
		}
	}
	return "", "", false
}
