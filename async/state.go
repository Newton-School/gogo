package async

import (
	"encoding/json"
	"time"
)

// InitialRecord creates a validated projection. Adapters must create-if-absent
// atomically and must not overwrite an existing worker's more recent state.
func InitialRecord(e Envelope, state State) (Record, error) {
	if err := e.Validate(); err != nil {
		return Record{}, err
	}
	if state != Queued && state != Scheduled {
		return Record{}, ErrInvalid
	}
	return Record{Envelope: cloneJSON(e), Digest: e.Digest(), State: state, Revision: 1, Pinned: e.WorkflowID != "", ReplayUntil: later(e.ETA, e.ExpiresAt)}, nil
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// ClaimRecord is the backend-neutral state machine. Its caller must compare the
// original revision and persist the returned record atomically. Durable adapters
// additionally enforce the server-time lease boundary in the committing script.
func ClaimRecord(record Record, e Envelope, owner string, now time.Time, lease time.Duration) (Claim, error) {
	if owner == "" || lease <= 0 {
		return Claim{}, ErrInvalid
	}
	if record.Digest != e.Digest() {
		return Claim{}, ErrConflict
	}
	if record.State.Terminal() || e.Retries < record.Envelope.Retries {
		return Claim{Record: record, Duplicate: true}, nil
	}
	if e.Retries > record.Envelope.Retries {
		return Claim{}, ErrConflict
	}
	if record.State == Running && record.LeaseUntil.After(now) {
		return Claim{Record: record}, nil
	}
	if record.Envelope.ETA.After(now) && !record.CancelRequested {
		return Claim{Record: record}, nil
	}
	record = cloneJSON(record)
	record.State = Running
	record.Owner = owner
	record.Fence++
	record.Revision++
	record.DeliveryCount++
	record.LeaseUntil = now.Add(lease)
	return Claim{Record: record, Acquired: true}, nil
}

func ValidateLease(record Record, fence uint64, owner string, now time.Time) error {
	if record.State != Running || record.Fence != fence || record.Owner != owner || !record.LeaseUntil.After(now) {
		return ErrLeaseLost
	}
	return nil
}

func ApplyTransition(record Record, t Transition, now time.Time, resultTTL, tombstoneTTL time.Duration) (Record, error) {
	if err := ValidateLease(record, t.Fence, t.Owner, now); err != nil {
		return Record{}, err
	}
	if t.ID != record.Envelope.ID || (!t.State.Terminal() && t.State != RetryWait) {
		return Record{}, ErrInvalid
	}
	if len(t.Output) > MaxPayloadBytes || (len(t.Output) > 0 && !json.Valid(t.Output)) {
		return Record{}, ErrInvalid
	}
	if t.State == Succeeded && len(t.Output) == 0 {
		return Record{}, ErrInvalid
	}
	if t.State == RetryWait {
		if t.Next == nil || t.Next.ID != record.Envelope.ID || t.Next.Retries != record.Envelope.Retries+1 || t.Next.Digest() != record.Digest || len(t.Intents) == 0 {
			return Record{}, ErrInvalid
		}
		if err := t.Next.Validate(); err != nil {
			return Record{}, err
		}
	}
	record = cloneJSON(record)
	record.State = t.State
	record.Revision++
	record.Output = append(json.RawMessage(nil), t.Output...)
	record.Failure = t.Failure
	record.Owner = ""
	record.LeaseUntil = time.Time{}
	if t.Next != nil {
		record.Envelope = cloneJSON(*t.Next)
		record.ReplayUntil = later(record.ReplayUntil, later(t.Next.ETA, t.Next.ExpiresAt))
	}
	if t.State.Terminal() {
		record.FinishedAt = now
		record.PayloadExpiresAt = now.Add(resultTTL)
		record.TombstoneUntil = later(now, record.ReplayUntil).Add(tombstoneTTL)
	}
	return record, nil
}

func CompletionIntents(e Envelope, state State, output json.RawMessage, failure *Failure) []Intent {
	if e.WorkflowID == "" || !state.Terminal() {
		return nil
	}
	return []Intent{{ID: StableID(e.ID, "completion"), SourceID: e.ID, Kind: "completion", WorkflowID: e.WorkflowID, Completion: &Completion{ID: e.ID, State: state, Output: output, Failure: failure}}}
}
