package async

import (
	"encoding/json"
	"time"
	"unicode/utf8"
)

func resultRecord(record Record, id string) (Record, error) {
	if record.Envelope.ID != id || record.Revision == 0 || record.DeliveryCount < 0 || len(record.Owner) > MaxPayloadBytes || !utf8.ValidString(record.Owner) {
		return Record{}, ErrUnavailable
	}
	switch record.State {
	case Scheduled, Queued, Running, RetryWait, Succeeded, Failed, Revoked, Expired:
	default:
		return Record{}, ErrUnavailable
	}
	if record.ReplacementID != "" && !idPattern.MatchString(record.ReplacementID) || len(record.Output) > MaxPayloadBytes || !utf8.Valid(record.Output) || len(record.Output) != 0 && !json.Valid(record.Output) || len(record.Progress) > 16<<10 || !utf8.Valid(record.Progress) || len(record.Progress) != 0 && !json.Valid(record.Progress) {
		return Record{}, ErrUnavailable
	}
	if f := record.Failure; f != nil && (record.State == Succeeded || f.Code == "" || len(f.Code) > 256 || len(f.Message) > 4096 || !utf8.ValidString(f.Code) || !utf8.ValidString(f.Message)) {
		return Record{}, ErrUnavailable
	}
	if !boundedResultEnvelope(record.Envelope) || record.Envelope.Validate() != nil || record.Digest != record.Envelope.Digest() {
		return Record{}, ErrUnavailable
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 3*MaxPayloadBytes {
		return Record{}, ErrUnavailable
	}
	var detached Record
	if json.Unmarshal(raw, &detached) != nil {
		return Record{}, ErrUnavailable
	}
	// time's JSON decoder can reuse cached unnamed fixed-zone locations.
	// Their internals are private, but the *Location value itself is writable.
	// Isolate that public surface before any authorization callback runs.
	detachResultTimes(&detached)
	return detached, nil
}

func detachResultTimes(record *Record) {
	detachEnvelopeTimes(&record.Envelope)
	for _, value := range []*time.Time{&record.LeaseUntil, &record.FinishedAt, &record.PayloadExpiresAt, &record.TombstoneUntil, &record.ReplayUntil} {
		detachWireTime(value)
	}

}

func detachWireTime(value *time.Time) {
	location := value.Location()
	_ = location.String()
	detached := *location
	*value = value.In(&detached)
}

func detachEnvelopeTimes(value *Envelope) {
	detachWireTime(&value.CreatedAt)
	detachWireTime(&value.ETA)
	detachWireTime(&value.ExpiresAt)
	for i := range value.Callbacks {
		detachSignatureTimes(&value.Callbacks[i])
	}
	for i := range value.Errbacks {
		detachSignatureTimes(&value.Errbacks[i])
	}
}

func detachSignatureTimes(value *Signature) {
	detachWireTime(&value.Options.ETA)
	detachWireTime(&value.Options.ExpiresAt)
	for i := range value.Callbacks {
		detachSignatureTimes(&value.Callbacks[i])
	}
	for i := range value.Errbacks {
		detachSignatureTimes(&value.Errbacks[i])
	}
}

// Charge a lower bound of the wire size before JSON encoding. This limits work
// on retained provider metadata (including cyclic/deep signature slices) without
// introducing a new signature codec or executing application methods.
func boundedResultEnvelope(e Envelope) bool {
	remaining := MaxPayloadBytes
	charge := func(text ...string) bool {
		for _, value := range text {
			if len(value) > remaining || !utf8.ValidString(value) {
				return false
			}
			remaining -= len(value)
		}
		remaining--
		return remaining >= 0
	}
	metadata := func(values map[string]string) bool {
		if len(values) > remaining {
			return false
		}
		for key, value := range values {
			if !charge(key, value) {
				return false
			}
		}
		return true
	}
	payload := func(value []byte) bool {
		if len(value) > remaining || !utf8.Valid(value) {
			return false
		}
		remaining -= len(value)
		return true
	}
	if !charge(e.ID, e.Task, e.Queue, e.WorkflowID, e.ParentID, e.RootID, e.Scope, e.Principal, e.IdempotencyKey, e.CorrelationID) || !payload(e.Args) || !metadata(e.Headers) || !metadata(e.TraceContext) || !metadata(e.Stamps) {
		return false
	}
	var links func([]Signature, int) bool
	links = func(values []Signature, depth int) bool {
		if len(values) > 32 || len(values) != 0 && depth > 16 {
			return false
		}
		for _, value := range values {
			o := value.Options
			if !charge(value.Task, value.ParentField, o.ID, o.Queue, o.Scope, o.Principal, o.IdempotencyKey, o.CorrelationID) || !payload(value.Args) || !metadata(o.Headers) || !metadata(o.Stamps) || !links(value.Callbacks, depth+1) || !links(value.Errbacks, depth+1) {
				return false
			}
		}
		return true
	}
	return links(e.Callbacks, 1) && links(e.Errbacks, 1)
}
