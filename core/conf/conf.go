// Package conf validates typed environment settings before resources are opened.
package conf

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Secret deliberately redacts formatting, JSON and structured logs. Reveal is
// restricted to the boundary that actually needs the secret (e.g. a connector).
type Secret struct{ value string }

func NewSecret(value string) Secret         { return Secret{value: value} }
func (Secret) String() string               { return "[REDACTED]" }
func (Secret) GoString() string             { return "[REDACTED]" }
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "[REDACTED]") }
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }
func (Secret) LogValue() slog.Value         { return slog.StringValue("[REDACTED]") }
func (s Secret) Reveal() string             { return s.value }

type Kind uint8

const (
	String Kind = iota
	Boolean
	Integer
	Duration
	List
	URL
)

type Definition struct {
	Name, Default, Group string
	Kind                 Kind
	Sensitive            bool
	RequiredFor          []string
	Min                  int64
	Choices              []string
}
type Schema []Definition
type Values struct{ entries map[string]any }

func (v Values) Get(name string) any {
	value := v.entries[name]
	if x, ok := value.([]string); ok {
		return slices.Clone(x)
	}
	return value
}
func (v Values) String(name string) string { value, _ := v.entries[name].(string); return value }
func (v Values) Secret(name string) Secret { value, _ := v.entries[name].(Secret); return value }
func (v Values) Bool(name string) bool     { value, _ := v.entries[name].(bool); return value }
func (v Values) Int(name string) int64     { value, _ := v.entries[name].(int64); return value }
func (v Values) Duration(name string) time.Duration {
	value, _ := v.entries[name].(time.Duration)
	return value
}
func (v Values) List(name string) []string {
	value, _ := v.entries[name].([]string)
	return slices.Clone(value)
}
func (v Values) MarshalJSON() ([]byte, error) { return json.Marshal(v.entries) }

type ConfigError struct {
	Names []string
	Code  string
}

func (e *ConfigError) Error() string { return e.Code + ": " + strings.Join(e.Names, ", ") }

func (s Schema) Load(environment map[string]string, resources ...string) (Values, error) {
	definitions := make(map[string]Definition, len(s))
	values := Values{entries: make(map[string]any, len(s))}
	var invalid, missing []string
	for _, d := range s {
		if _, exists := definitions[d.Name]; exists || !strings.HasPrefix(d.Name, "GOGO_") {
			return Values{}, errors.New("invalid configuration schema")
		}
		definitions[d.Name] = d
		raw, ok := environment[d.Name]
		if !ok {
			raw = d.Default
		}
		if raw == "" {
			for _, role := range d.RequiredFor {
				if slices.Contains(resources, role) {
					missing = append(missing, d.Name)
					break
				}
			}
		}
		value, err := parse(d, raw)
		if err != nil {
			invalid = append(invalid, d.Name)
		} else {
			values.entries[d.Name] = value
		}
	}
	for key := range environment {
		if strings.HasPrefix(key, "GOGO_") {
			if _, ok := definitions[key]; !ok {
				invalid = append(invalid, key)
			}
		}
	}
	if len(invalid) > 0 {
		slices.Sort(invalid)
		return Values{}, &ConfigError{Names: slices.Compact(invalid), Code: "CONFIG_INVALID"}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return Values{}, &ConfigError{Names: slices.Compact(missing), Code: "CONFIG_REQUIRED"}
	}
	if key := values.Secret("GOGO_SECRET_KEY").Reveal(); key != "" && len(key) < 32 {
		return Values{}, &ConfigError{Names: []string{"GOGO_SECRET_KEY"}, Code: "CONFIG_INVALID"}
	}
	if values.Int("GOGO_DB_MAX_IDLE") > values.Int("GOGO_DB_MAX_OPEN") {
		return Values{}, &ConfigError{Names: []string{"GOGO_DB_MAX_IDLE"}, Code: "CONFIG_INVALID"}
	}
	return values, nil
}
func parse(d Definition, raw string) (any, error) {
	if len(d.Choices) > 0 && !slices.Contains(d.Choices, raw) {
		return nil, errors.New("invalid choice")
	}
	if d.Sensitive {
		return NewSecret(raw), nil
	}
	switch d.Kind {
	case Boolean:
		return strconv.ParseBool(raw)
	case Integer:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && n < d.Min {
			err = errors.New("below minimum")
		}
		return n, err
	case Duration:
		n, err := time.ParseDuration(raw)
		if err == nil && n < time.Duration(d.Min) {
			err = errors.New("below minimum")
		}
		return n, err
	case List:
		var out []string
		for _, v := range strings.Split(raw, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out, nil
	case URL:
		if raw != "" {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme == "" {
				return nil, errors.New("invalid URL")
			}
		}
		return raw, nil
	default:
		return raw, nil
	}
}

// ReadEnv parses literal KEY=value entries. It never expands shell expressions,
// command substitutions or other environment variables.
func ReadEnv(r io.Reader) (map[string]string, error) {
	result := map[string]string{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		v := strings.TrimSpace(scanner.Text())
		if v == "" || strings.HasPrefix(v, "#") {
			continue
		}
		key, value, ok := strings.Cut(v, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || !validName(key) {
			return nil, fmt.Errorf("invalid environment entry at line %d", line)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate environment key at line %d", line)
		}
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return nil, fmt.Errorf("invalid environment quoting at line %d", line)
			}
			value = value[1 : len(value)-1]
		}
		result[key] = value
	}
	return result, scanner.Err()
}
func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r != '_' && (r < 'A' || r > 'Z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}
func Environment(entries []string) map[string]string {
	out := map[string]string{}
	for _, e := range entries {
		if k, v, ok := strings.Cut(e, "="); ok {
			out[k] = v
		}
	}
	return out
}
func Merge(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		for k, v := range layer {
			out[k] = v
		}
	}
	return out
}

func (s Schema) EnvTemplate() string {
	var b strings.Builder
	last := ""
	for _, d := range s {
		if d.Group != last {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString("# " + d.Group + "\n")
			last = d.Group
		}
		b.WriteString(d.Name + "=" + d.Default + "\n")
	}
	return b.String()
}
