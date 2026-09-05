package async

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

func (r *Registry) validateLinks(s Signature, depth int) error {
	if depth > 16 || len(s.Callbacks)+len(s.Errbacks) > 32 {
		return ErrInvalid
	}
	parent, err := r.lookup(s.Task, s.Version)
	if err != nil {
		return err
	}
	for kind, links := range [][]Signature{s.Callbacks, s.Errbacks} {
		for _, link := range links {
			definition, err := r.lookup(link.Task, link.Version)
			if err != nil {
				return err
			}
			if !link.Immutable {
				target, err := inputBindingType(definition, link)
				if err != nil {
					return err
				}
				source := parent.output
				if kind == 1 {
					source = reflect.TypeFor[Failure]()
				}
				if target != source {
					return fmt.Errorf("%w: incompatible callback input", ErrInvalid)
				}
			}
			if err := r.validateLinks(link, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *Worker) linkedIntents(parent Envelope, t Transition) ([]Intent, error) {
	links := parent.Callbacks
	value := t.Output
	kind := "callback"
	if t.State != Succeeded {
		links = parent.Errbacks
		kind = "errback"
		var err error
		value, err = json.Marshal(t.Failure)
		if err != nil {
			return nil, err
		}
	}
	var intents []Intent
	for index, link := range links {
		bound, err := link.bind(value)
		if err != nil {
			return nil, err
		}
		definition, err := w.Registry.lookup(link.Task, link.Version)
		if err != nil {
			return nil, err
		}
		// Invalid callback input remains an independently failed task. It does not
		// undo the parent's already successful handler effects.
		queue := bound.Options.Queue
		if queue == "" {
			queue = definition.options.Queue
		}
		id := StableID(parent.ID, fmt.Sprintf("%s-%d", kind, index))
		created := w.Clock().UTC()
		eta := bound.Options.ETA
		if bound.Options.Countdown > 0 {
			eta = created.Add(bound.Options.Countdown)
		}
		e := Envelope{ProtocolVersion: ProtocolVersion, ID: id, Task: bound.Task, Version: bound.Version, Args: bound.Args, CreatedAt: created, Queue: queue, MaxRetries: definition.options.Retry.MaxRetries, ETA: eta, ExpiresAt: bound.Options.ExpiresAt, Priority: bound.Options.Priority, Scope: parent.Scope, Principal: parent.Principal, ParentID: parent.ID, RootID: parent.RootID, Headers: bound.Options.Headers, Stamps: bound.Options.Stamps, Callbacks: bound.Callbacks, Errbacks: bound.Errbacks}
		e.ReplacementDepth = parent.ReplacementDepth
		if e.RootID == "" {
			e.RootID = parent.ID
		}
		if e.ETA.Before(created) {
			e.ETA = time.Time{}
		}
		if err := e.Validate(); err != nil {
			return nil, err
		}
		intent := dispatchIntent(parent.ID, e)
		if err := definition.validate(e.Args); err != nil {
			intent.Kind = "fail"
		}
		intents = append(intents, intent)
	}
	return intents, nil
}
