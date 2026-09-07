package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	ghttp "github.com/Newton-School/gogo/core/http"
)

type entityCondition struct {
	present, any bool
	tags         []string
}

// RFC 9110 entity tags are not quoted strings: backslashes are literal, and
// commas within the opaque tag must not split the list. Weak tags are valid
// syntax but can never satisfy If-Match's strong comparison.
func parseIfMatch(headers http.Header) (entityCondition, error) {
	values := headers.Values("If-Match")
	condition := entityCondition{present: len(values) != 0}
	if !condition.present {
		return condition, nil
	}
	invalid := mediaError(400, "INVALID_PRECONDITION", "Invalid If-Match condition")
	if len(values) > 32 {
		return entityCondition{}, invalid
	}
	value := strings.Join(values, ",")
	if len(value) > 4096 {
		return entityCondition{}, invalid
	}
	value = strings.Trim(value, " \t")
	if value == "*" {
		condition.any = true
		return condition, nil
	}
	elements := 0
	for value != "" {
		elements++
		if elements > 64 {
			return entityCondition{}, invalid
		}
		value = strings.TrimLeft(value, " \t")
		if value == "" {
			break
		}
		if value[0] == ',' {
			value = value[1:]
			continue
		}
		weak := strings.HasPrefix(value, "W/")
		if weak {
			value = value[2:]
		}
		if len(value) == 0 || value[0] != '"' {
			return entityCondition{}, invalid
		}
		end := strings.IndexByte(value[1:], '"')
		if end < 0 {
			return entityCondition{}, invalid
		}
		end++
		for _, b := range []byte(value[1:end]) {
			if b < 0x21 || b == 0x7f {
				return entityCondition{}, invalid
			}
		}
		if !weak {
			condition.tags = append(condition.tags, value[:end+1])
		}
		value = strings.TrimLeft(value[end+1:], " \t")
		if value != "" {
			if value[0] != ',' {
				return entityCondition{}, invalid
			}
			value = value[1:]
		}
	}
	return condition, nil
}

func (condition entityCondition) matches(tag string) bool {
	if !condition.present || condition.any {
		return true
	}
	for _, candidate := range condition.tags {
		if candidate == tag {
			return true
		}
	}
	return false
}

func representationTag(value Values) (string, error) {
	response, err := ghttp.JSON(http.StatusOK, value)
	if err != nil {
		return "", err
	}
	return bodyTag(response.Body), nil
}

func bodyTag(body []byte) string {
	digest := sha256.Sum256(body)
	return `"` + hex.EncodeToString(digest[:]) + `"`
}

type taggedRepresentation struct{ body Values }
