package templates

import (
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (r *renderer) renderTemplate(nodes []node, data Context, overrides map[string][]node, out *strings.Builder, depth int) error {
	if depth >= r.engine.config.MaxDepth {
		return fmt.Errorf("templates: maximum depth exceeded")
	}
	if overrides == nil {
		overrides = map[string][]node{}
	}
	for _, n := range nodes {
		if n.kind == "extends" {
			next := map[string][]node{}
			for k, v := range overrides {
				next[k] = v
			}
			for _, block := range nodes {
				if block.kind == "block" {
					name := strings.Fields(block.arg)
					if len(name) != 1 {
						return ErrRender
					}
					if _, exists := next[name[0]]; !exists {
						next[name[0]] = block.children
					}
				}
			}
			parent, err := r.eval(n.arg, data)
			if err != nil {
				return err
			}
			base, err := r.engine.load(r.ctx, fmt.Sprint(parent))
			if err != nil {
				return err
			}
			return r.renderTemplate(base, data, next, out, depth+1)
		}
	}
	return r.render(nodes, data, overrides, out, depth)
}
func (r *renderer) render(nodes []node, data Context, overrides map[string][]node, out *strings.Builder, depth int) error {
	if depth >= r.engine.config.MaxDepth {
		return ErrRender
	}
	for _, n := range nodes {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		switch n.kind {
		case "text":
			writeText(out, n.arg)
		case "value":
			if n.arg == "block.super" {
				if super, ok := data["__block_super"].([]node); ok {
					if err := r.render(super, data, nil, out, depth+1); err != nil {
						return err
					}
					continue
				}
			}
			value, err := r.eval(n.arg, data)
			if err != nil {
				return err
			}
			if !r.autoescape {
				switch v := value.(type) {
				case SafeHTML:
					value = template.HTML(v)
				case template.HTML:
					value = v
				default:
					value = template.HTML(fmt.Sprint(value))
				}
			}
			if err = r.emit(out, value); err != nil {
				return err
			}
		case "if":
			ok, err := r.condition(n.arg, data)
			if err != nil {
				return err
			}
			body := n.alternate
			if ok {
				body = n.children
			}
			if err = r.render(body, data, overrides, out, depth+1); err != nil {
				return err
			}
		case "for":
			words := splitQuoted(n.arg, ' ')
			at := slices.Index(words, "in")
			if at < 1 || at+1 >= len(words) {
				return ErrRender
			}
			names := strings.Split(strings.Join(words[:at], ""), ",")
			rest := words[at+1:]
			reversed := len(rest) > 1 && rest[len(rest)-1] == "reversed"
			if reversed {
				rest = rest[:len(rest)-1]
			}
			value, err := r.eval(strings.Join(rest, " "), data)
			if err != nil {
				return err
			}
			items := sequence(value)
			if reversed {
				slices.Reverse(items)
			}
			if len(items) == 0 {
				if err = r.render(n.alternate, data, overrides, out, depth+1); err != nil {
					return err
				}
			}
			for i, item := range items {
				r.iterations++
				if r.iterations > r.engine.config.MaxIterations {
					return ErrRender
				}
				local := copyContext(data)
				if len(names) == 1 {
					local[names[0]] = item
				} else {
					parts := sequence(item)
					if len(parts) != len(names) {
						return ErrRender
					}
					for j, name := range names {
						local[name] = parts[j]
					}
				}
				local["forloop"] = Context{"counter": i + 1, "counter0": i, "revcounter": len(items) - i, "revcounter0": len(items) - i - 1, "first": i == 0, "last": i == len(items)-1, "parentloop": data["forloop"]}
				if err = r.render(n.children, local, overrides, out, depth+1); err != nil {
					return err
				}
			}
		case "block":
			names := strings.Fields(n.arg)
			if len(names) != 1 {
				return ErrRender
			}
			body := n.children
			local := data
			if replacement, ok := overrides[names[0]]; ok {
				body = replacement
				local = copyContext(data)
				local["__block_super"] = n.children
			}
			if err := r.render(body, local, nil, out, depth+1); err != nil {
				return err
			}
		case "include":
			words := splitQuoted(n.arg, ' ')
			if len(words) == 0 {
				return ErrRender
			}
			name, err := r.eval(words[0], data)
			if err != nil {
				return err
			}
			local := copyContext(data)
			only := words[len(words)-1] == "only"
			if only {
				local = Context{}
				words = words[:len(words)-1]
			}
			for _, assignment := range words[1:] {
				if assignment == "with" {
					continue
				}
				pair := strings.SplitN(assignment, "=", 2)
				if len(pair) != 2 {
					return ErrRender
				}
				v, err := r.eval(pair[1], data)
				if err != nil {
					return err
				}
				local[pair[0]] = v
			}
			parts := strings.SplitN(fmt.Sprint(name), "#", 2)
			child, err := r.engine.load(r.ctx, parts[0])
			if err != nil {
				return err
			}
			if len(parts) == 2 {
				child = findNamed(child, "partialdef", parts[1])
				if child == nil {
					return ErrNotFound
				}
			}
			if err = r.renderTemplate(child, local, nil, out, depth+1); err != nil {
				return err
			}
		case "with":
			local := copyContext(data)
			words := splitQuoted(n.arg, ' ')
			if len(words) == 3 && words[1] == "as" {
				v, err := r.eval(words[0], data)
				if err != nil {
					return err
				}
				local[words[2]] = v
			} else {
				for _, assignment := range words {
					pair := strings.SplitN(assignment, "=", 2)
					if len(pair) != 2 {
						return ErrRender
					}
					v, err := r.eval(pair[1], data)
					if err != nil {
						return err
					}
					local[pair[0]] = v
				}
			}
			if err := r.render(n.children, local, overrides, out, depth+1); err != nil {
				return err
			}
		case "autoescape":
			if n.arg != "on" && n.arg != "off" {
				return ErrRender
			}
			previous := r.autoescape
			r.autoescape = n.arg == "on"
			err := r.render(n.children, data, overrides, out, depth+1)
			r.autoescape = previous
			if err != nil {
				return err
			}
		case "comment":
			continue
		case "partialdef":
			words := strings.Fields(n.arg)
			if len(words) == 2 && words[1] == "inline" {
				if err := r.render(n.children, data, overrides, out, depth+1); err != nil {
					return err
				}
			}
		case "load":
			for _, library := range strings.Fields(n.arg) {
				if !slices.Contains(r.engine.config.Libraries, library) {
					return fmt.Errorf("templates: unknown library %s", library)
				}
			}
		case "csrf_token":
			token, err := r.eval("csrf_token", data)
			if err != nil {
				return err
			}
			if token != nil && token != "" {
				writeText(out, `<input type="hidden" name="csrfmiddlewaretoken" value="`)
				if err = r.emit(out, token); err != nil {
					return err
				}
				writeText(out, `">`)
			}
		case "firstof":
			for _, arg := range splitQuoted(n.arg, ' ') {
				v, err := r.eval(arg, data)
				if err != nil {
					return err
				}
				if truthy(v) {
					if err = r.emit(out, v); err != nil {
						return err
					}
					break
				}
			}
		case "now":
			words := splitQuoted(n.arg, ' ')
			if len(words) < 1 {
				return ErrRender
			}
			format, err := r.eval(words[0], data)
			if err != nil {
				return err
			}
			value := formatDate(time.Now(), fmt.Sprint(format))
			if len(words) == 3 && words[1] == "as" {
				data[words[2]] = value
			} else if err = r.emit(out, value); err != nil {
				return err
			}
		case "widthratio":
			words := splitQuoted(n.arg, ' ')
			if len(words) != 3 {
				return ErrRender
			}
			nums := make([]float64, 3)
			for i, w := range words {
				v, err := r.eval(w, data)
				if err != nil {
					return err
				}
				number, ok := number(v)
				if !ok {
					return ErrRender
				}
				nums[i] = number
			}
			if nums[1] == 0 {
				return ErrRender
			}
			if err := r.emit(out, strconv.FormatFloat(nums[0]/nums[1]*nums[2], 'f', 0, 64)); err != nil {
				return err
			}
		case "templatetag":
			v, ok := map[string]string{"openblock": "{%", "closeblock": "%}", "openvariable": "{{", "closevariable": "}}", "openbrace": "{", "closebrace": "}", "opencomment": "{#", "closecomment": "#}"}[n.arg]
			if !ok {
				return ErrRender
			}
			writeText(out, v)
		case "spaceless":
			var inner strings.Builder
			if err := r.render(n.children, data, overrides, &inner, depth+1); err != nil {
				return err
			}
			out.WriteString(betweenTags.ReplaceAllString(inner.String(), "><"))
		case "debug":
			if !r.engine.config.Debug {
				return ErrRender
			}
			if err := r.emit(out, "Template debugging enabled"); err != nil {
				return err
			}
		case "querystring":
			query, ok := data["querystring"].(url.Values)
			if !ok {
				query = url.Values{}
			}
			result := url.Values{}
			for k, v := range query {
				result[k] = append([]string(nil), v...)
			}
			for _, assignment := range splitQuoted(n.arg, ' ') {
				pair := strings.SplitN(assignment, "=", 2)
				if len(pair) != 2 {
					return ErrRender
				}
				v, err := r.eval(pair[1], data)
				if err != nil {
					return err
				}
				if v == nil {
					result.Del(pair[0])
				} else {
					result.Set(pair[0], fmt.Sprint(v))
				}
			}
			q := result.Encode()
			if q != "" {
				q = "?" + q
			}
			if err := r.emit(out, q); err != nil {
				return err
			}
		default:
			tag, ok := r.engine.config.Tags[n.kind]
			if !ok {
				return fmt.Errorf("templates: unknown tag %s", n.kind)
			}
			args := []any{}
			words := splitQuoted(n.arg, ' ')
			assign := ""
			if len(words) >= 2 && words[len(words)-2] == "as" {
				assign = words[len(words)-1]
				words = words[:len(words)-2]
			}
			for _, arg := range words {
				v, err := r.eval(arg, data)
				if err != nil {
					return err
				}
				args = append(args, v)
			}
			value, err := tag(r.ctx, copyContext(data), args)
			if err != nil {
				return ErrRender
			}
			if assign != "" {
				data[assign] = value
			} else if err = r.emit(out, value); err != nil {
				return err
			}
		}
		if out.Len() > 16<<20 {
			return ErrRender
		}
	}
	return nil
}

var betweenTags = regexp.MustCompile(`>\s+<`)
