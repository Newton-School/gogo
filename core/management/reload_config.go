package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/conf"
)

const reloadConfigLimit = 1 << 20

type reloadConfigBuffer struct{ data []byte }

func (b *reloadConfigBuffer) Write(p []byte) (int, error) {
	if len(p) > reloadConfigLimit-len(b.data) {
		return 0, errors.New("candidate configuration exceeds limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func validateReloadCandidate(ctx context.Context, name string, i Invocation, entry *runServerEntry) (conf.Values, error) {
	run := func(argument string, stdout io.Writer) (err error) {
		if err = reloadContextError(ctx); err != nil {
			return err
		}
		command := exec.Command(name, argument)
		command.Dir, command.Env = i.Project.Root, slices.Clone(entry.environment)
		command.Stdout, command.Stderr = stdout, i.Stderr
		process, err := startReloadProcess(command)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, process.stop(reloadWaitDelay)) }()
		select {
		case <-process.done:
			return process.err
		case <-ctx.Done():
			return reloadContextError(ctx)
		}
	}
	if err := run("check", i.Stdout); err != nil {
		return conf.Values{}, err
	}
	var output reloadConfigBuffer
	if err := run("diffsettings", &output); err != nil {
		return conf.Values{}, err
	}
	settings, err := parseReloadConfiguration(output.data)
	if err != nil {
		return conf.Values{}, err
	}
	return settings, reloadContextError(ctx)
}

// Only framework-owned conf.Values encodings are accepted: an object of
// scalar or scalar-list values. No captured setting is printed or returned.
func parseReloadConfiguration(data []byte) (conf.Values, error) {
	invalid := func() (conf.Values, error) { return conf.Values{}, errors.New("invalid candidate configuration") }
	if len(data) > reloadConfigLimit || !utf8.Valid(data) || !json.Valid(data) || !reloadJSONStrings(data) {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return invalid()
	}
	seen := map[string]bool{}
	values := map[string]string{}
	stringsWanted := map[string]bool{"GOGO_ENV": true, "GOGO_HTTP_ADDR": true, "GOGO_STORAGE_ROOT": true, "GOGO_STATIC_ROOT": true}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return invalid()
		}
		name, ok := key.(string)
		if !ok || len(name) > 1024 || seen[name] || len(seen) >= 4096 {
			return invalid()
		}
		seen[name] = true
		value, err := decoder.Token()
		if err != nil {
			return invalid()
		}
		if delimiter, ok := value.(json.Delim); ok {
			if delimiter != '[' {
				return invalid()
			}
			for count := 0; decoder.More(); count++ {
				if count >= 65536 {
					return invalid()
				}
				item, e := decoder.Token()
				if e != nil {
					return invalid()
				}
				if _, complex := item.(json.Delim); complex {
					return invalid()
				}
			}
			end, e := decoder.Token()
			if e != nil || end != json.Delim(']') {
				return invalid()
			}
			if stringsWanted[name] || name == "GOGO_SHUTDOWN_GRACE" {
				return invalid()
			}
			continue
		}
		if stringsWanted[name] {
			text, ok := value.(string)
			if !ok || len(text) > 4096 {
				return invalid()
			}
			values[name] = text
		}
		if name == "GOGO_SHUTDOWN_GRACE" {
			number, ok := value.(json.Number)
			if !ok {
				return invalid()
			}
			n, e := strconv.ParseInt(string(number), 10, 64)
			if e != nil || n <= 0 {
				return invalid()
			}
			values[name] = strconv.FormatInt(n, 10) + "ns"
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return invalid()
	}
	if _, err = decoder.Token(); err != io.EOF {
		return invalid()
	}
	if !seen["GOGO_ENV"] || !seen["GOGO_SHUTDOWN_GRACE"] {
		return invalid()
	}
	settings, err := conf.CoreSchema().Load(values)
	if err != nil {
		return invalid()
	}
	if settings.String("GOGO_ENV") == "production" {
		return invalid()
	}
	if _, err = reloadGrace(settings); err != nil {
		return invalid()
	}
	return settings, nil
}

// encoding/json accepts unpaired UTF-16 escapes by replacement. Reject them
// before decoding the private subprocess result instead of normalizing paths.
func reloadJSONStrings(data []byte) bool {
	inside := false
	for index := 0; index < len(data); index++ {
		if data[index] == '"' {
			inside = !inside
			continue
		}
		if !inside || data[index] != '\\' {
			continue
		}
		index++
		if index >= len(data) {
			return false
		}
		if data[index] != 'u' {
			continue
		}
		if index+4 >= len(data) {
			return false
		}
		n, err := strconv.ParseUint(string(data[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		index += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if index+6 >= len(data) || data[index+1] != '\\' || data[index+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[index+3:index+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			index += 6
		}
	}
	return !inside
}
