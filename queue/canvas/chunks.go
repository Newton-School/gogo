package canvas

import q "github.com/Newton-School/gogo/queue"

func NewChunks(taskName string, values []any, size int) Group {
	if size < 1 {
		size = 1
	}
	tasks := make([]Signature, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunk := append([]any(nil), values[start:end]...)
		tasks = append(tasks, Task(q.NewSignature(taskName, chunk)))
	}
	return NewGroup(tasks...)
}
