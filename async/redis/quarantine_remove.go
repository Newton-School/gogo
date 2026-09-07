package redis

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/async"
	redigo "github.com/redis/go-redis/v9"
)

var removeQuarantineScript = redigo.NewScript(`
local kind=redis.call('TYPE',KEYS[1]).ok
if kind=='none' then return 'ABSENT' end
if kind~='stream' then error('invalid quarantine key type') end
local rows=redis.call('XRANGE',KEYS[1],ARGV[1],ARGV[1],'COUNT',1)
if #rows==0 then return 'ABSENT' end
local values={};local allowed={receipt=true,code=true,digest=true,priority=true}
local fields=rows[1][2]
for i=1,#fields,2 do
 local key=fields[i]
 if not allowed[key] or values[key]~=nil then error('unsupported quarantine metadata') end
 values[key]=fields[i+1]
end
if not values.receipt or not values.code or not values.digest then error('incomplete quarantine metadata') end
if values.priority~=nil and not string.match(values.priority,'^[0-9]$') then error('invalid quarantine priority') end
local priority=values.priority or '-1'
if rows[1][1]~=ARGV[1] or values.receipt~=ARGV[2] or values.code~=ARGV[3] or values.digest~=ARGV[4] or priority~=ARGV[5] then return 'CONFLICT' end
if redis.call('XDEL',KEYS[1],ARGV[1])~=1 then error('quarantine removal not confirmed') end
return 'REMOVED'
`)

// RemoveQuarantine is a trusted maintenance port; clients use Control for the
// distinct exact-entry authorization grant. Only the diagnostic stream appears
// in this atomic operation's keys. No message body is retrieved or requeued.
func (b *Broker) RemoveQuarantine(ctx context.Context, entry async.QuarantineEntry) (bool, error) {
	if ctx == nil || b == nil || b.valid() != nil || !b.allowed(entry.Record.Queue) || async.ValidateQuarantineEntry(entry) != nil {
		return false, async.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	position, err := b.quarantinePosition(entry.Record.Queue, entry.Cursor)
	if err != nil || position == "" || position != entry.Record.ID || !validStreamCursor(entry.Record.SourceReceipt) {
		return false, async.ErrInvalid
	}
	millis, _, _ := strings.Cut(position, "-")
	ms, err := strconv.ParseInt(millis, 10, 64)
	if err != nil || !entry.Record.FirstSeen.Equal(time.UnixMilli(ms).UTC()) {
		return false, async.ErrInvalid
	}
	out, err := b.Connection.Atomic(ctx, removeQuarantineScript, []string{b.queuePrefix(entry.Record.Queue) + ":quarantine"}, position, entry.Record.SourceReceipt, entry.Record.Reason, entry.Record.Digest, strconv.Itoa(entry.Record.Priority))
	if err != nil {
		return false, err
	}
	switch out {
	case "REMOVED":
		return true, nil
	case "ABSENT":
		return false, nil
	case "CONFLICT":
		return false, async.ErrConflict
	default:
		return false, async.ErrUnavailable
	}
}

var _ async.QuarantineRemover = (*Broker)(nil)
