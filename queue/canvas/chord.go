package canvas

import (
	"context"

	"github.com/google/uuid"

	q "github.com/Newton-School/gogo/queue"
)

type Chord struct {
	Header Group
	Body   Signature
}

func NewChord(header Group, body Signature) Chord {
	return Chord{Header: header, Body: body}
}

func (c Chord) Serialize() SerializedWorkflow {
	body := serializeCanvasSignature(c.Body)
	return SerializedWorkflow{
		Type:  "chord",
		Tasks: serializeCanvasSignatures(c.Header.Tasks),
		Body:  &body,
	}
}

func (c Chord) ApplyAsync(ctx context.Context, options ApplyOptions) (ApplyResult, error) {
	chordID := options.ChordID
	if chordID == "" {
		chordID = uuid.NewString()
	}
	options.ChordID = chordID
	result, err := c.Header.ApplyAsync(ctx, options)
	result.ChordID = chordID
	return result, err
}

func (c Chord) Complete(ctx context.Context, options ApplyOptions, values []any) (ApplyResult, error) {
	chordID := options.ChordID
	if chordID == "" {
		chordID = uuid.NewString()
	}
	body := c.Body.Signature.Clone()
	body.Kwargs = cloneAnyMap(body.Kwargs)
	if body.Kwargs == nil {
		body.Kwargs = map[string]any{}
	}
	if !c.Body.ImmutableFlag {
		body.Kwargs["chord_results"] = append([]any(nil), values...)
	}
	message, err := options.App.SendTask(ctx, options.Broker, body, q.SendOptions{
		Router:  options.Router,
		ID:      uuid.NewString(),
		ChordID: chordID,
	})
	if err != nil {
		return ApplyResult{ChordID: chordID}, err
	}
	return ApplyResult{ChordID: chordID, TaskIDs: []string{message.Envelope.ID}}, nil
}
