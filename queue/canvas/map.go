package canvas

import q "github.com/Newton-School/gogo/queue"

func NewMap(taskName string, values []any) Group {
	tasks := make([]Signature, len(values))
	for i, value := range values {
		tasks[i] = Task(q.NewSignature(taskName, value))
	}
	return NewGroup(tasks...)
}

func NewStarmap(taskName string, values [][]any) Group {
	tasks := make([]Signature, len(values))
	for i, args := range values {
		tasks[i] = Task(q.NewSignature(taskName, args...))
	}
	return NewGroup(tasks...)
}
