package canvas

import (
	"context"

	"github.com/google/uuid"

	q "github.com/Newton-School/gogo/queue"
)

type Group struct {
	Tasks    []Signature
	Callback []q.Signature
	Errback  []q.Signature
}

func NewGroup(tasks ...Signature) Group {
	return Group{Tasks: cloneCanvasSignatures(tasks)}
}

func (g Group) Link(callback q.Signature) Group {
	g.Callback = append(cloneQueueSignatures(g.Callback), callback.Clone())
	return g
}

func (g Group) LinkError(errback q.Signature) Group {
	g.Errback = append(cloneQueueSignatures(g.Errback), errback.Clone())
	return g
}

func (g Group) Serialize() SerializedWorkflow {
	return SerializedWorkflow{
		Type:      "group",
		Tasks:     serializeCanvasSignatures(g.Tasks),
		Callbacks: serializeQueueSignatures(g.Callback),
		Errbacks:  serializeQueueSignatures(g.Errback),
	}
}

func (g Group) ApplyAsync(ctx context.Context, options ApplyOptions) (ApplyResult, error) {
	groupID := options.GroupID
	if groupID == "" {
		groupID = uuid.NewString()
	}
	result := ApplyResult{GroupID: groupID, ChordID: options.ChordID}
	for _, task := range g.Tasks {
		message, err := options.App.SendTask(ctx, options.Broker, task.Signature, q.SendOptions{
			Router:  options.Router,
			ID:      uuid.NewString(),
			GroupID: groupID,
			ChordID: options.ChordID,
		})
		if err != nil {
			_ = dispatchErrbacks(ctx, options, append(task.Errbacks, g.Errback...))
			return result, err
		}
		result.TaskIDs = append(result.TaskIDs, message.Envelope.ID)
	}
	if options.Backend != nil {
		if _, err := options.Backend.GroupResult(ctx, groupID, result.TaskIDs); err != nil {
			return result, err
		}
	}
	for _, callback := range g.Callback {
		message, err := options.App.SendTask(ctx, options.Broker, callback, q.SendOptions{Router: options.Router, GroupID: groupID})
		if err != nil {
			_ = dispatchErrbacks(ctx, options, g.Errback)
			return result, err
		}
		result.TaskIDs = append(result.TaskIDs, message.Envelope.ID)
	}
	return result, nil
}
