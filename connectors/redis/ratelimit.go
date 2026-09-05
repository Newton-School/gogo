package redis

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/core/ratelimit"
	redigo "github.com/redis/go-redis/v9"
)

type Limiter struct{ Connection *Connection }

var limitScript = redigo.NewScript(`
local tm=redis.call('TIME');local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
local rate=tonumber(ARGV[1]);local burst=tonumber(ARGV[2]);local period=tonumber(ARGV[3]);local cost=tonumber(ARGV[4])
local old=redis.call('HMGET',KEYS[1],'tokens','at')
local tokens=burst;if old[1] then tokens=math.min(burst,tonumber(old[1])+math.max(0,now-tonumber(old[2]))*rate/period) end
local allowed=0;local retry=0;if tokens>=cost then allowed=1;tokens=tokens-cost else retry=math.ceil((cost-tokens)*period/rate) end
redis.call('HSET',KEYS[1],'tokens',tokens,'at',now);redis.call('PEXPIRE',KEYS[1],math.max(1,math.ceil(burst*period/rate)))
return {allowed,math.floor(tokens),retry}
`)

func (l *Limiter) Allow(ctx context.Context, key string, limit ratelimit.Limit, cost int) (ratelimit.Decision, error) {
	if err := limit.Validate(cost); err != nil {
		return ratelimit.Decision{}, err
	}
	if l.Connection == nil || limit.Period < time.Millisecond {
		return ratelimit.Decision{}, ErrInvalid
	}
	periodMillis := float64(limit.Period) / float64(time.Millisecond)
	refillMillis := float64(limit.Burst) * periodMillis / limit.Rate
	// Lua numbers are IEEE-754 doubles. Reject non-representable quotas before
	// HSET, rather than partially mutating a bucket then failing PEXPIRE or
	// overflowing the public RetryAfter duration.
	if uint64(limit.Burst) > 1<<53-1 || math.IsInf(refillMillis, 0) || math.IsNaN(refillMillis) || refillMillis > float64(math.MaxInt64/int64(time.Millisecond))-1 {
		return ratelimit.Decision{}, ErrInvalid
	}
	out, err := l.Connection.Atomic(ctx, limitScript, []string{l.Connection.namespace + ":limit:{" + Digest(key) + "}"}, strconv.FormatFloat(limit.Rate, 'g', -1, 64), limit.Burst, strconv.FormatFloat(periodMillis, 'g', -1, 64), cost)
	if err != nil {
		return ratelimit.Decision{}, err
	}
	values := out.([]any)
	return ratelimit.Decision{Allowed: values[0].(int64) == 1, Remaining: int(values[1].(int64)), RetryAfter: time.Duration(values[2].(int64)) * time.Millisecond}, nil
}

var _ ratelimit.Limiter = (*Limiter)(nil)
