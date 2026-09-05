package templates

import (
	"fmt"
	"regexp"
	"strings"
)

type token struct{ kind, text string }
type node struct {
	kind, arg           string
	children, alternate []node
}
type parser struct {
	tokens []token
	index  int
	depth  int
}

func lex(source string) ([]token, error) {
	tokens := []token{}
	for len(source) > 0 {
		start := -1
		kind := ""
		end := ""
		for _, pair := range []struct{ open, close, kind string }{{"{{", "}}", "value"}, {"{%", "%}", "tag"}, {"{#", "#}", "comment"}} {
			if i := strings.Index(source, pair.open); i >= 0 && (start < 0 || i < start) {
				start = i
				kind = pair.kind
				end = pair.close
			}
		}
		if start < 0 {
			tokens = append(tokens, token{"text", source})
			break
		}
		if start > 0 {
			tokens = append(tokens, token{"text", source[:start]})
		}
		source = source[start+2:]
		stop := strings.Index(source, end)
		if stop < 0 {
			return nil, fmt.Errorf("templates: unclosed %s", kind)
		}
		content := strings.TrimSpace(source[:stop])
		source = source[stop+2:]
		if kind == "comment" {
			continue
		}
		if kind == "tag" {
			words := strings.Fields(content)
			if len(words) > 0 && (words[0] == "comment" || words[0] == "verbatim") {
				closing := "end" + words[0]
				if words[0] == "verbatim" && len(words) > 1 {
					closing += " " + strings.Join(words[1:], " ")
				}
				start, end := rawEnd(source, closing)
				if start < 0 {
					return nil, fmt.Errorf("templates: unclosed %s", words[0])
				}
				if words[0] == "verbatim" {
					tokens = append(tokens, token{"text", source[:start]})
				}
				source = source[end:]
				continue
			}
		}
		tokens = append(tokens, token{kind, content})
	}
	return tokens, nil
}

func rawEnd(source, closing string) (int, int) {
	parts := strings.Fields(closing)
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	match := regexp.MustCompile(`\{%\s*` + strings.Join(parts, `\s+`) + `\s*%\}`).FindStringIndex(source)
	if match == nil {
		return -1, -1
	}
	return match[0], match[1]
}
func parse(source string) ([]node, error) {
	tokens, err := lex(source)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: tokens}
	nodes, stop, err := p.sequence(nil)
	if err == nil && stop != "" {
		err = fmt.Errorf("templates: unexpected closing tag")
	}
	return nodes, err
}
func (p *parser) sequence(stops map[string]bool) ([]node, string, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 128 {
		return nil, "", ErrRender
	}
	nodes := []node{}
	for p.index < len(p.tokens) {
		t := p.tokens[p.index]
		p.index++
		if t.kind != "tag" {
			nodes = append(nodes, node{kind: t.kind, arg: t.text})
			continue
		}
		words := strings.Fields(t.text)
		if len(words) == 0 {
			return nil, "", fmt.Errorf("templates: empty tag")
		}
		kind := words[0]
		arg := strings.TrimSpace(strings.TrimPrefix(t.text, kind))
		if stops[kind] {
			return nodes, t.text, nil
		}
		n := node{kind: kind, arg: arg}
		var err error
		var stop string
		switch kind {
		case "if":
			n, err = p.ifNode(arg)
		case "for":
			n.children, stop, err = p.sequence(map[string]bool{"empty": true, "endfor": true})
			if stop == "empty" {
				n.alternate, stop, err = p.sequence(map[string]bool{"endfor": true})
			}
			if stop == "" && err == nil {
				err = fmt.Errorf("templates: unclosed for")
			}
		case "ifchanged":
			n.children, stop, err = p.sequence(map[string]bool{"else": true, "endifchanged": true})
			if stop == "else" {
				n.alternate, stop, err = p.sequence(map[string]bool{"endifchanged": true})
			}
			if stop == "" && err == nil {
				err = fmt.Errorf("templates: unclosed ifchanged")
			}
		case "block", "with", "autoescape", "filter", "spaceless", "comment", "partialdef":
			n.children, stop, err = p.sequence(map[string]bool{"end" + kind: true})
			if stop == "" && err == nil {
				err = fmt.Errorf("templates: unclosed %s", kind)
			}
		default:
			if strings.HasPrefix(kind, "end") || kind == "else" || kind == "elif" || kind == "empty" {
				return nil, "", fmt.Errorf("templates: unexpected %s", kind)
			}
		}
		if err != nil {
			return nil, "", err
		}
		nodes = append(nodes, n)
	}
	return nodes, "", nil
}

func (p *parser) ifNode(condition string) (node, error) {
	n := node{kind: "if", arg: condition}
	children, stop, err := p.sequence(map[string]bool{"elif": true, "else": true, "endif": true})
	n.children = children
	if err != nil {
		return n, err
	}
	if strings.HasPrefix(stop, "elif ") {
		child, err := p.ifNode(strings.TrimPrefix(stop, "elif "))
		if err != nil {
			return n, err
		}
		n.alternate = []node{child}
	} else if stop == "else" {
		n.alternate, stop, err = p.sequence(map[string]bool{"endif": true})
	}
	if stop == "" {
		return n, fmt.Errorf("templates: unclosed if")
	}
	return n, err
}

// splitQuoted preserves quoted strings while splitting expressions and tag args.
func splitQuoted(s string, separator rune) []string {
	var parts []string
	start := 0
	quote := rune(0)
	escaped := false
	for i, ch := range s {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == separator {
			part := strings.TrimSpace(s[start:i])
			if part != "" {
				parts = append(parts, part)
			}
			start = i + 1
		}
	}
	part := strings.TrimSpace(s[start:])
	if part != "" {
		parts = append(parts, part)
	}
	return parts
}
