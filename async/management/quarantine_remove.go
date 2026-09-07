package management

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/Newton-School/gogo/async"
)

// queues quarantine-remove '<one complete QuarantineEntry JSON>' requires the
// full inspected identity, not a queue wildcard or an arbitrary deletion ID.
func quarantineRemovalArguments(args []string) (async.QuarantineEntry, error) {
	var entry async.QuarantineEntry
	if len(args) != 2 || args[0] != "quarantine-remove" || len(args[1]) > 16<<10 {
		return entry, async.ErrInvalid
	}
	fields, err := quarantineRemovalObject(args[1], "Cursor", "Record")
	if err != nil {
		return entry, async.ErrInvalid
	}
	if _, err := quarantineRemovalObject(string(fields["Record"]), "ID", "Queue", "SourceReceipt", "Reason", "Digest", "FirstSeen", "Priority"); err != nil {
		return entry, async.ErrInvalid
	}
	if json.Unmarshal([]byte(args[1]), &entry) != nil || async.ValidateQuarantineEntry(entry) != nil {
		return async.QuarantineEntry{}, async.ErrInvalid
	}
	return entry, nil
}

// Inspected entries have a fixed two-level schema. Require every canonical key
// exactly once, including zero-valued Priority; encoding/json's case-insensitive
// matching and last-key-wins behavior are not safe identity selection rules.
func quarantineRemovalObject(input string, names ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(input))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, async.ErrInvalid
	}
	fields := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		fields[name] = nil
	}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, async.ErrInvalid
		}
		value, exists := fields[name]
		if !exists || value != nil {
			return nil, async.ErrInvalid
		}
		if decoder.Decode(&value) != nil || string(value) == "null" {
			return nil, async.ErrInvalid
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, async.ErrInvalid
	}
	for _, value := range fields {
		if value == nil {
			return nil, async.ErrInvalid
		}
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, async.ErrInvalid
	}
	return fields, nil
}
