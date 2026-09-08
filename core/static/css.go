package static

import (
	"context"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// cssReference distinguishes explicitly static references from external URLs.
// Resolution uses logical slash paths, never source/destination filesystem paths.
func (c Collector) cssReference(sheet, raw string) (name, suffix string, local bool, err error) {
	if raw == "" || len(raw) > 4096 || !utf8.ValidString(raw) {
		return "", "", false, ErrDependency
	}
	for _, ch := range raw {
		if ch < 32 || ch == 127 || ch == '\\' {
			return "", "", false, ErrDependency
		}
	}
	if strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "//") {
		return "", "", false, nil
	}
	u, e := url.Parse(raw)
	if e != nil {
		return "", "", false, ErrDependency
	}
	if u.Scheme != "" {
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			if c.prefix.scheme == "" || !strings.EqualFold(u.Scheme, c.prefix.scheme) || !strings.EqualFold(u.Host, c.prefix.host) || u.User != nil || !strings.HasPrefix(u.Path, c.prefix.path) {
				return "", "", false, nil
			}
			// An exact configured CDN origin/prefix is still a local static
			// dependency. Resolve it from declared sources, never by fetching.
			u.Scheme, u.Host = "", ""
		case "data", "blob":
			return "", "", false, nil
		default:
			return "", "", false, ErrDependency
		}
	}
	if u.Host != "" || u.User != nil || u.Opaque != "" {
		return "", "", false, ErrDependency
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		suffix = raw[i:]
	}
	if strings.HasPrefix(u.Path, "/") {
		if !strings.HasPrefix(u.Path, c.prefix.path) {
			return "", "", false, nil
		}
		name = path.Clean(strings.TrimPrefix(u.Path, c.prefix.path))
	} else if u.Path == "" {
		name = sheet
	} else {
		name = path.Join(path.Dir(sheet), u.Path)
	}
	if !validPath(name) || ignoredPath(name) {
		return "", "", false, ErrDependency
	}
	return name, suffix, true, nil
}

func relativeAsset(directory, target string) string {
	var from []string
	if directory != "." {
		from = strings.Split(directory, "/")
	}
	to := strings.Split(target, "/")
	common := 0
	for common < len(from) && common < len(to) && from[common] == to[common] {
		common++
	}
	parts := make([]string, 0, len(from)+len(to))
	for range from[common:] {
		parts = append(parts, "..")
	}
	for _, part := range to[common:] {
		parts = append(parts, url.PathEscape(part))
	}
	return strings.Join(parts, "/")
}

func cssSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
func cssName(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c >= 128 || c == '\\'
}
func cssHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func cssEscape(source string, index int, continuation bool) (string, int, error) {
	index++
	if index >= len(source) {
		return "", 0, ErrDependency
	}
	if source[index] == '\n' || source[index] == '\r' || source[index] == '\f' {
		if !continuation {
			return "", 0, ErrDependency
		}
		if source[index] == '\r' && index+1 < len(source) && source[index+1] == '\n' {
			index++
		}
		return "", index + 1, nil
	}
	if cssHex(source[index]) {
		end := index
		for end < len(source) && end-index < 6 && cssHex(source[end]) {
			end++
		}
		value, e := strconv.ParseInt(source[index:end], 16, 32)
		if e != nil || value == 0 || value > utf8.MaxRune || value >= 0xd800 && value <= 0xdfff {
			return "", 0, ErrDependency
		}
		if end < len(source) && cssSpace(source[end]) {
			if source[end] == '\r' && end+1 < len(source) && source[end+1] == '\n' {
				end++
			}
			end++
		}
		return string(rune(value)), end, nil
	}
	ch, size := utf8.DecodeRuneInString(source[index:])
	if ch < 32 || ch == 127 {
		return "", 0, ErrDependency
	}
	return string(ch), index + size, nil
}
func cssIdentifier(source string, index int) (string, int, error) {
	var out strings.Builder
	for index < len(source) && cssName(source[index]) {
		if source[index] == '\\' {
			value, end, err := cssEscape(source, index, false)
			if err != nil {
				return "", 0, err
			}
			out.WriteString(value)
			index = end
		} else {
			out.WriteByte(source[index])
			index++
		}
	}
	return out.String(), index, nil
}
func cssString(source string, index int) (string, int, error) {
	quote := source[index]
	index++
	var out strings.Builder
	for index < len(source) {
		ch := source[index]
		if ch == quote {
			return out.String(), index + 1, nil
		}
		if ch == '\\' {
			value, end, err := cssEscape(source, index, true)
			if err != nil {
				return "", 0, err
			}
			out.WriteString(value)
			index = end
			continue
		}
		if ch == '\n' || ch == '\r' || ch == '\f' || ch == 0 {
			return "", 0, ErrDependency
		}
		out.WriteByte(ch)
		index++
	}
	return "", 0, ErrDependency
}
func cssComment(source string, index int) (int, error) {
	end := strings.Index(source[index+2:], "*/")
	if end < 0 {
		return 0, ErrDependency
	}
	end += index + 4
	// Source map rewriting and image-set string sources are not silently
	// treated as complete dependency support by this initial tokenizer.
	body := strings.TrimSpace(source[index+2 : end-2])
	if strings.HasPrefix(body, "#") || strings.HasPrefix(body, "@") {
		body = strings.TrimSpace(body[1:])
		if strings.HasPrefix(body, "sourceMappingURL=") {
			return 0, ErrDependency
		}
	}
	return end, nil
}
func cssTrivia(source string, index int) (int, error) {
	for index < len(source) {
		if cssSpace(source[index]) {
			index++
			continue
		}
		if strings.HasPrefix(source[index:], "/*") {
			end, err := cssComment(source, index)
			if err != nil {
				return 0, err
			}
			index = end
			continue
		}
		break
	}
	return index, nil
}
func cssURL(source string, index int) (string, int, error) {
	// index follows the opening parenthesis. Comments inside an unquoted URL
	// are URL characters, not whitespace; unsupported syntax fails explicitly.
	for index < len(source) && cssSpace(source[index]) {
		index++
	}
	if index >= len(source) {
		return "", 0, ErrDependency
	}
	if source[index] == '\'' || source[index] == '"' {
		value, end, err := cssString(source, index)
		if err != nil {
			return "", 0, err
		}
		for end < len(source) && cssSpace(source[end]) {
			end++
		}
		if end >= len(source) || source[end] != ')' {
			return "", 0, ErrDependency
		}
		return value, end + 1, nil
	}
	var out strings.Builder
	for index < len(source) {
		ch := source[index]
		if ch == ')' {
			return out.String(), index + 1, nil
		}
		if cssSpace(ch) {
			for index < len(source) && cssSpace(source[index]) {
				index++
			}
			if index >= len(source) || source[index] != ')' {
				return "", 0, ErrDependency
			}
			return out.String(), index + 1, nil
		}
		if ch == '\\' {
			value, end, err := cssEscape(source, index, false)
			if err != nil {
				return "", 0, err
			}
			out.WriteString(value)
			index = end
			continue
		}
		if ch == '(' || ch == '\'' || ch == '"' || ch < 32 || ch == 127 {
			return "", 0, ErrDependency
		}
		out.WriteByte(ch)
		index++
	}
	return "", 0, ErrDependency
}
func quoteCSS(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

// rewriteCSS is a bounded tokenizer, not a regex substitution or full CSS
// parser. It supports escaped url() identifiers and quoted/unquoted URL values,
// plus @import strings/url() without altering their qualifier tail. Unrelated
// strings and comments are copied exactly. Recognized unsupported dependency
// syntax fails closed, rather than leaving unhashed local references behind.
func rewriteCSS(ctx context.Context, data []byte, maximum int64, rewrite func(string) (string, error)) ([]byte, error) {
	if int64(len(data)) > maximum {
		return nil, ErrLimit
	}
	if !utf8.Valid(data) {
		return nil, ErrDependency
	}
	source := string(data)
	var out strings.Builder
	appendText := func(value string) error {
		if int64(len(value)) > maximum-int64(out.Len()) {
			return ErrLimit
		}
		out.WriteString(value)
		return nil
	}
	for i := 0; i < len(source); {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		start := i
		if strings.HasPrefix(source[i:], "/*") {
			end, err := cssComment(source, i)
			if err != nil {
				return nil, err
			}
			if err = appendText(source[i:end]); err != nil {
				return nil, err
			}
			i = end
			continue
		}
		if source[i] == '\'' || source[i] == '"' {
			_, end, err := cssString(source, i)
			if err != nil {
				return nil, err
			}
			if err = appendText(source[i:end]); err != nil {
				return nil, err
			}
			i = end
			continue
		}
		if source[i] == '@' {
			name, end, err := cssIdentifier(source, i+1)
			if err != nil {
				return nil, err
			}
			if strings.EqualFold(name, "import") {
				valueStart, err := cssTrivia(source, end)
				if err != nil || valueStart >= len(source) {
					return nil, ErrDependency
				}
				if source[valueStart] == '\'' || source[valueStart] == '"' {
					value, valueEnd, err := cssString(source, valueStart)
					if err != nil {
						return nil, err
					}
					replacement, err := rewrite(value)
					if err != nil {
						return nil, err
					}
					if err = appendText(source[start:valueStart]); err != nil {
						return nil, err
					}
					text := source[valueStart:valueEnd]
					if replacement != value {
						text = quoteCSS(replacement)
					}
					if err = appendText(text); err != nil {
						return nil, err
					}
					i = valueEnd
					continue
				}
				keyword, keywordEnd, err := cssIdentifier(source, valueStart)
				if err != nil || !strings.EqualFold(keyword, "url") || keywordEnd >= len(source) || source[keywordEnd] != '(' {
					return nil, ErrDependency
				}
			}
			if end > i+1 {
				if err = appendText(source[i:end]); err != nil {
					return nil, err
				}
				i = end
				continue
			}
		}
		if cssName(source[i]) {
			name, end, err := cssIdentifier(source, i)
			if err != nil {
				return nil, err
			}
			if end < len(source) && source[end] == '(' {
				if strings.EqualFold(name, "image-set") || strings.EqualFold(name, "-webkit-image-set") {
					return nil, ErrDependency
				}
				if strings.EqualFold(name, "url") {
					value, valueEnd, err := cssURL(source, end+1)
					if err != nil {
						return nil, err
					}
					replacement, err := rewrite(value)
					if err != nil {
						return nil, err
					}
					text := source[start:valueEnd]
					if replacement != value {
						text = source[start:end] + "(" + quoteCSS(replacement) + ")"
					}
					if err = appendText(text); err != nil {
						return nil, err
					}
					i = valueEnd
					continue
				}
			}
			if err = appendText(source[start:end]); err != nil {
				return nil, err
			}
			i = end
			continue
		}
		if source[i] == 0 {
			return nil, ErrDependency
		}
		if err := appendText(source[i : i+1]); err != nil {
			return nil, err
		}
		i++
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}
