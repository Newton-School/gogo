package templates

import (
	"html/template"
	"reflect"
	"slices"
	"strings"
)

type cycleState struct {
	expressions []string
	name        string
	index       int
	silent      bool
}

func (r *renderer) cycle(n *node, data Context, out *strings.Builder) error {
	if r.cycles == nil {
		r.cycles = map[*node]*cycleState{}
		r.namedCycles = map[string]*cycleState{}
	}
	words := splitQuoted(n.arg, ' ')
	if len(words) == 0 {
		return ErrRender
	}
	state, ok := r.cycles[n]
	if !ok {
		if len(words) == 1 {
			state = r.namedCycles[words[0]]
			if state == nil {
				return ErrRender
			}
		} else {
			state = &cycleState{}
			if at := slices.Index(words, "as"); at >= 0 {
				if at < 2 || at+1 >= len(words) || len(words) > at+3 {
					return ErrRender
				}
				state.name = words[at+1]
				if len(words) == at+3 {
					if words[at+2] != "silent" {
						return ErrRender
					}
					state.silent = true
				}
				words = words[:at]
				r.namedCycles[state.name] = state
			}
			state.expressions = append([]string(nil), words...)
		}
		r.cycles[n] = state
	}
	value, err := r.eval(state.expressions[state.index%len(state.expressions)], data)
	if err != nil {
		return err
	}
	state.index++
	r.lastCycle = state
	if state.name != "" {
		data[state.name] = value
	}
	if !state.silent {
		return r.emit(out, value)
	}
	return nil
}

func (r *renderer) materialize(source string) (string, error) {
	t, err := template.New("fragment").Parse(source)
	if err != nil {
		return "", ErrRender
	}
	out := boundedOutput{ctx: r.ctx, remaining: 16 << 20}
	if err = t.Execute(&out, r.values); err != nil {
		return "", ErrRender
	}
	return out.String(), nil
}

func (r *renderer) ifchanged(n *node, data Context, overrides map[string][]node, out *strings.Builder, depth int) error {
	if r.changed == nil {
		r.changed = map[*node]any{}
	}
	var value any
	var rendered strings.Builder
	words := splitQuoted(n.arg, ' ')
	if len(words) == 0 {
		if err := r.render(n.children, data, overrides, &rendered, depth+1); err != nil {
			return err
		}
		text, err := r.materialize(rendered.String())
		if err != nil {
			return err
		}
		value = text
	} else {
		items := make([]any, len(words))
		for i, word := range words {
			item, err := r.eval(word, data)
			if err != nil {
				return err
			}
			items[i] = item
		}
		value = items
	}
	previous, seen := r.changed[n]
	changed := !seen || !reflect.DeepEqual(previous, value)
	r.changed[n] = value
	if !changed {
		return r.render(n.alternate, data, overrides, out, depth+1)
	}
	if len(words) == 0 {
		out.WriteString(rendered.String())
		return nil
	}
	return r.render(n.children, data, overrides, out, depth+1)
}

func (r *renderer) regroup(arg string, data Context) error {
	words := splitQuoted(arg, ' ')
	if len(words) != 5 || words[1] != "by" || words[3] != "as" {
		return ErrRender
	}
	value, err := r.eval(words[0], data)
	if err != nil {
		return err
	}
	groups := []any{}
	var group Context
	for _, item := range sequence(value) {
		r.iterations++
		if r.iterations > r.engine.config.MaxIterations {
			return ErrRender
		}
		if err := r.ctx.Err(); err != nil {
			return err
		}
		local := copyContext(data)
		local["group_item"] = item
		key, err := r.eval("group_item."+words[2], local)
		if err != nil {
			return err
		}
		if group == nil || !equalValues(group["grouper"], key) {
			group = Context{"grouper": key, "list": []any{}}
			groups = append(groups, group)
		}
		group["list"] = append(group["list"].([]any), item)
	}
	data[words[4]] = groups
	return nil
}
