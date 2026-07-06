package canvas

import (
	"context"

	"github.com/google/uuid"

	q "github.com/Newton-School/gogo/queue"
)

type Signature struct {
	Signature     q.Signature
	ImmutableFlag bool
	Callbacks     []q.Signature
	Errbacks      []q.Signature
}

func Task(signature q.Signature) Signature {
	return Signature{Signature: signature.Clone()}
}

func (s Signature) ImmutableSignature() Signature {
	s.ImmutableFlag = true
	return s
}

func (s Signature) ImmutableTask() Signature {
	return s.ImmutableSignature()
}

func (s Signature) ImmutableClone() Signature {
	return s.ImmutableSignature()
}

func (s Signature) Immutable() Signature {
	return s.ImmutableSignature()
}

func (s Signature) Link(callback q.Signature) Signature {
	s.Callbacks = append(cloneQueueSignatures(s.Callbacks), callback.Clone())
	return s
}

func (s Signature) LinkError(errback q.Signature) Signature {
	s.Errbacks = append(cloneQueueSignatures(s.Errbacks), errback.Clone())
	return s
}

type SerializedSignature struct {
	Name      string                `json:"name"`
	Args      []any                 `json:"args,omitempty"`
	Kwargs    map[string]any        `json:"kwargs,omitempty"`
	Headers   map[string]string     `json:"headers,omitempty"`
	Immutable bool                  `json:"immutable,omitempty"`
	Callbacks []SerializedSignature `json:"callbacks,omitempty"`
	Errbacks  []SerializedSignature `json:"errbacks,omitempty"`
}

type SerializedWorkflow struct {
	Type      string                `json:"type"`
	Tasks     []SerializedSignature `json:"tasks,omitempty"`
	Body      *SerializedSignature  `json:"body,omitempty"`
	Callbacks []SerializedSignature `json:"callbacks,omitempty"`
	Errbacks  []SerializedSignature `json:"errbacks,omitempty"`
	ChunkSize int                   `json:"chunk_size,omitempty"`
}

type ApplyOptions struct {
	App     *q.App
	Broker  q.Broker
	Backend q.ResultBackend
	Router  *q.Router
	GroupID string
	ChordID string
	RootID  string
}

type ApplyResult struct {
	TaskIDs []string
	GroupID string
	ChordID string
}

type Chain struct {
	Tasks     []Signature
	Callbacks []q.Signature
	Errbacks  []q.Signature
}

func NewChain(tasks ...Signature) Chain {
	return Chain{Tasks: cloneCanvasSignatures(tasks)}
}

func (c Chain) Link(callback q.Signature) Chain {
	c.Callbacks = append(cloneQueueSignatures(c.Callbacks), callback.Clone())
	return c
}

func (c Chain) LinkError(errback q.Signature) Chain {
	c.Errbacks = append(cloneQueueSignatures(c.Errbacks), errback.Clone())
	return c
}

func (c Chain) Serialize() SerializedWorkflow {
	return SerializedWorkflow{
		Type:      "chain",
		Tasks:     serializeCanvasSignatures(c.Tasks),
		Callbacks: serializeQueueSignatures(c.Callbacks),
		Errbacks:  serializeQueueSignatures(c.Errbacks),
	}
}

func (c Chain) ApplyAsync(ctx context.Context, options ApplyOptions) (ApplyResult, error) {
	result := ApplyResult{GroupID: options.GroupID, ChordID: options.ChordID}
	rootID := options.RootID
	parentID := ""
	for _, task := range c.Tasks {
		id := uuid.NewString()
		if rootID == "" {
			rootID = id
		}
		message, err := options.App.SendTask(ctx, options.Broker, task.Signature, q.SendOptions{
			Router:   options.Router,
			ID:       id,
			RootID:   rootID,
			ParentID: parentID,
			GroupID:  options.GroupID,
			ChordID:  options.ChordID,
		})
		if err != nil {
			_ = dispatchErrbacks(ctx, options, append(task.Errbacks, c.Errbacks...))
			return result, err
		}
		result.TaskIDs = append(result.TaskIDs, message.Envelope.ID)
		parentID = message.Envelope.ID
	}
	for _, callback := range c.Callbacks {
		callbackMessage, err := options.App.SendTask(ctx, options.Broker, callback, q.SendOptions{Router: options.Router, RootID: rootID, ParentID: parentID})
		if err != nil {
			_ = dispatchErrbacks(ctx, options, c.Errbacks)
			return result, err
		}
		result.TaskIDs = append(result.TaskIDs, callbackMessage.Envelope.ID)
	}
	return result, nil
}

func dispatchErrbacks(ctx context.Context, options ApplyOptions, errbacks []q.Signature) error {
	for _, errback := range errbacks {
		if _, err := options.App.SendTask(ctx, options.Broker, errback, q.SendOptions{Router: options.Router}); err != nil {
			return err
		}
	}
	return nil
}

func serializeCanvasSignatures(signatures []Signature) []SerializedSignature {
	serialized := make([]SerializedSignature, len(signatures))
	for i, signature := range signatures {
		serialized[i] = serializeCanvasSignature(signature)
	}
	return serialized
}

func serializeCanvasSignature(signature Signature) SerializedSignature {
	serialized := serializeQueueSignature(signature.Signature)
	serialized.Immutable = signature.ImmutableFlag
	serialized.Callbacks = serializeQueueSignatures(signature.Callbacks)
	serialized.Errbacks = serializeQueueSignatures(signature.Errbacks)
	return serialized
}

func serializeQueueSignatures(signatures []q.Signature) []SerializedSignature {
	serialized := make([]SerializedSignature, len(signatures))
	for i, signature := range signatures {
		serialized[i] = serializeQueueSignature(signature)
	}
	return serialized
}

func serializeQueueSignature(signature q.Signature) SerializedSignature {
	return SerializedSignature{
		Name:    signature.Name,
		Args:    append([]any(nil), signature.Args...),
		Kwargs:  cloneAnyMap(signature.Kwargs),
		Headers: cloneStringMap(signature.Headers),
	}
}

func cloneCanvasSignatures(signatures []Signature) []Signature {
	cloned := make([]Signature, len(signatures))
	for i, signature := range signatures {
		cloned[i] = Signature{
			Signature:     signature.Signature.Clone(),
			ImmutableFlag: signature.ImmutableFlag,
			Callbacks:     cloneQueueSignatures(signature.Callbacks),
			Errbacks:      cloneQueueSignatures(signature.Errbacks),
		}
	}
	return cloned
}

func cloneQueueSignatures(signatures []q.Signature) []q.Signature {
	cloned := make([]q.Signature, len(signatures))
	for i, signature := range signatures {
		cloned[i] = signature.Clone()
	}
	return cloned
}

func cloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
