package redis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

// Control records share their worker's Redis partition, never a task/result
// partition. Bodies remain opaque base64 JSON; Lua only handles tiny string
// metadata, bounded wall-clock milliseconds and exact ownership digests.
const controlPrelude = `
local clock=redis.call('TIME');local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
local function millis(s)
 if type(s)~='string' or #s>16 or not string.match(s,'^[1-9][0-9]*$') then error('invalid control timestamp') end
 local n=tonumber(s);if not n or n>9007199254740991 or string.format('%.0f',n)~=s then error('invalid control timestamp') end
 return n
end
local function decode(raw)
 if #raw>24000 then error('oversized worker control') end
 local d=cjson.decode(raw)
 if type(d)~='table' or type(d.body)~='string' or type(d.digest)~='string' or #d.digest~=64 or type(d.instance)~='string' or #d.instance~=36 or type(d.outcome)~='string' or type(d.replied)~='string' then error('invalid worker control') end
 millis(d.expires);millis(d.retained)
 if d.outcome=='' then if d.replied~='' then error('invalid worker control reply') end
 elseif d.outcome=='accepted' or d.outcome=='rejected_queue_changed' then millis(d.replied)
 else error('invalid worker control outcome') end
 return d
end
local function all()
 local entries=redis.call('HGETALL',KEYS[2]);if #entries>256 then error('worker control bound exceeded') end
 local docs={};for i=1,#entries,2 do docs[entries[i]]=decode(entries[i+1]) end
 return docs
end
local function owns(token,instance)
 local p=redis.call('HMGET',KEYS[1],'owner','instance','status','expires')
 return p[1] and p[1]==token and p[2]==instance and instance~='' and p[3]=='online' and p[4] and millis(p[4])>now
end
`

var controlSubmit = redigo.NewScript(controlPrelude + `
local docs=all();local old=docs[ARGV[1]]
if old and millis(old.retained)>now then if old.digest==ARGV[2] then return {'OK'} else return {'CONFLICT'} end end
local expires=millis(ARGV[5]);if expires<=now or expires-now>300000 then return {'INVALID'} end
local p=redis.call('HMGET',KEYS[1],'instance','status','expires')
if not p[1] or p[1]~=ARGV[4] or p[2]~='online' or not p[3] or millis(p[3])<=now then return {'LOST'} end
local count=0;for id,d in pairs(docs) do if millis(d.retained)>now then count=count+1 end end
if count>=128 then return {'CONFLICT'} end
local document=cjson.encode({body=ARGV[3],digest=ARGV[2],instance=ARGV[4],expires=ARGV[5],retained=string.format('%.0f',now+86400000),outcome='',replied=''})
for id,d in pairs(docs) do if millis(d.retained)<=now then redis.call('HDEL',KEYS[2],id) end end
redis.call('HSET',KEYS[2],ARGV[1],document);redis.call('PEXPIRE',KEYS[2],86400000)
return {'OK'}
`)

var controlReceive = redigo.NewScript(controlPrelude + `
local instance=redis.call('HGET',KEYS[1],'instance')
if not instance or not owns(ARGV[1],instance) then return {'LOST'} end
local docs=all();local ids={}
for id,d in pairs(docs) do if d.instance==instance and (d.outcome=='' or d.outcome=='accepted') and millis(d.expires)>now and millis(d.retained)>now then table.insert(ids,id) end end
table.sort(ids);local out={'OK',instance}
for i=1,math.min(#ids,tonumber(ARGV[2])) do table.insert(out,ids[i]);table.insert(out,docs[ids[i]].body);table.insert(out,docs[ids[i]].digest) end
return out
`)

var controlReply = redigo.NewScript(controlPrelude + `
if not owns(ARGV[1],ARGV[4]) then return {'LOST'} end
local raw=redis.call('HGET',KEYS[2],ARGV[2]);if not raw then return {'MISSING'} end
local d=decode(raw);if millis(d.retained)<=now then return {'MISSING'} end
if d.digest~=ARGV[3] or d.instance~=ARGV[4] then return {'CONFLICT'} end
if d.outcome~='' then if d.outcome~=ARGV[5] then return {'CONFLICT'} else return {'OK',d.outcome,d.replied} end end
if millis(d.expires)<=now then return {'MISSING'} end
d.outcome=ARGV[5];d.replied=string.format('%.0f',now)
redis.call('HSET',KEYS[2],ARGV[2],cjson.encode(d))
return {'OK',d.outcome,d.replied}
`)

var controlLookup = redigo.NewScript(controlPrelude + `
local raw=redis.call('HGET',KEYS[2],ARGV[1]);if not raw then return {'MISSING'} end
local d=decode(raw);if millis(d.retained)<=now then return {'MISSING'} end
if d.digest~=ARGV[2] then return {'CONFLICT'} end
if d.outcome=='' then return {'MISSING'} end
return {'OK',d.outcome,d.replied}
`)

func (w *Workers) controlKeys(id string) []string {
	return []string{w.key(id), w.Connection.PartitionKey("worker", id, "controls")}
}

func controlResult(value any, err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	items, ok := value.([]any)
	if !ok || len(items) < 1 {
		return nil, async.ErrUnavailable
	}
	fields := make([]string, len(items))
	for i, item := range items {
		var ok bool
		fields[i], ok = item.(string)
		if !ok {
			return nil, async.ErrUnavailable
		}
	}
	switch fields[0] {
	case "OK":
		return fields[1:], nil
	case "CONFLICT":
		return nil, async.ErrConflict
	case "INVALID":
		return nil, async.ErrInvalid
	case "LOST":
		return nil, async.ErrLeaseLost
	case "MISSING":
		return nil, async.ErrNotFound
	default:
		return nil, async.ErrUnavailable
	}
}

func (w *Workers) SubmitWorkerControl(ctx context.Context, request async.WorkerControlRequest) error {
	if w == nil || w.Connection == nil {
		return async.ErrInvalid
	}
	digest, err := async.WorkerControlDigest(request)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(request)
	_, err = controlResult(w.Connection.Atomic(ctx, controlSubmit, w.controlKeys(request.WorkerID), request.ID, digest, base64.StdEncoding.EncodeToString(data), request.InstanceID, request.ExpiresAt.UnixMilli()))
	return err
}

func (w *Workers) ReceiveWorkerControls(ctx context.Context, lease async.WorkerLease, limit int) ([]async.WorkerControlRequest, error) {
	if w == nil || w.Connection == nil || async.ValidateWorkerControlReceiver(lease, limit) != nil {
		return nil, async.ErrInvalid
	}
	fields, err := controlResult(w.Connection.Atomic(ctx, controlReceive, w.controlKeys(lease.WorkerID), connector.Digest(lease.Token), limit))
	if err != nil {
		return nil, err
	}
	if len(fields) < 1 || (len(fields)-1)%3 != 0 || (len(fields)-1)/3 > limit {
		return nil, async.ErrUnavailable
	}
	instance := fields[0]
	fields = fields[1:]
	requests := make([]async.WorkerControlRequest, 0, len(fields)/3)
	for i := 0; i < len(fields); i += 3 {
		if len(fields[i+1]) > 22000 {
			return nil, async.ErrUnavailable
		}
		body, err := base64.StdEncoding.DecodeString(fields[i+1])
		if err != nil || len(body) > 16*1024 {
			return nil, async.ErrUnavailable
		}
		var request async.WorkerControlRequest
		if json.Unmarshal(body, &request) != nil || request.WorkerID != lease.WorkerID || request.InstanceID != instance || request.ID != fields[i] {
			return nil, async.ErrUnavailable
		}
		digest, err := async.WorkerControlDigest(request)
		if err != nil || digest != fields[i+2] {
			return nil, async.ErrUnavailable
		}
		requests = append(requests, request)
	}
	return requests, nil
}

func controlReplyValue(request async.WorkerControlRequest, fields []string, err error) (async.WorkerControlReply, error) {
	if err != nil {
		return async.WorkerControlReply{}, err
	}
	if len(fields) != 2 {
		return async.WorkerControlReply{}, async.ErrUnavailable
	}
	at, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || at <= 0 {
		return async.WorkerControlReply{}, async.ErrUnavailable
	}
	reply := async.WorkerControlReply{RequestID: request.ID, WorkerID: request.WorkerID, InstanceID: request.InstanceID, Command: request.Command, Outcome: async.WorkerControlOutcome(fields[0]), At: time.UnixMilli(at).UTC()}
	if async.ValidateWorkerControlReply(request, reply) != nil {
		return async.WorkerControlReply{}, async.ErrUnavailable
	}
	return reply, nil
}

func (w *Workers) ReplyWorkerControl(ctx context.Context, lease async.WorkerLease, request async.WorkerControlRequest, outcome async.WorkerControlOutcome) (async.WorkerControlReply, error) {
	if w == nil || w.Connection == nil || async.ValidateWorkerControlReceiver(lease, 1) != nil || lease.WorkerID != request.WorkerID || outcome != async.ControlAccepted && outcome != async.ControlQueueChanged {
		return async.WorkerControlReply{}, async.ErrInvalid
	}
	digest, err := async.WorkerControlDigest(request)
	if err != nil {
		return async.WorkerControlReply{}, err
	}
	fields, err := controlResult(w.Connection.Atomic(ctx, controlReply, w.controlKeys(request.WorkerID), connector.Digest(lease.Token), request.ID, digest, request.InstanceID, string(outcome)))
	return controlReplyValue(request, fields, err)
}

func (w *Workers) LookupWorkerControl(ctx context.Context, request async.WorkerControlRequest) (async.WorkerControlReply, error) {
	if w == nil || w.Connection == nil {
		return async.WorkerControlReply{}, async.ErrInvalid
	}
	digest, err := async.WorkerControlDigest(request)
	if err != nil {
		return async.WorkerControlReply{}, err
	}
	fields, err := controlResult(w.Connection.Atomic(ctx, controlLookup, w.controlKeys(request.WorkerID), request.ID, digest))
	return controlReplyValue(request, fields, err)
}

var _ async.WorkerControlStore = (*Workers)(nil)
