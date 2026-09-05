package redis

import "sync/atomic"

// Rotate the first partition independently for each backend operation. A
// permanently busy earlier partition cannot win every bounded batch: every
// partition starts first once per 64 calls. The counter is local scheduling
// state, not a durable cursor or ownership token. Wraparound preserves modulo
// 64 because 64 divides the uint32 range exactly.
type partitionRotation struct{ next atomic.Uint32 }

func (r *partitionRotation) start() int { return int((r.next.Add(1) - 1) % 64) }
