package redis

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/Newton-School/gogo/async"
)

// Lua may update only this small metadata document. Payload is base64-encoded
// JSON bytes: cjson must never interpret task arguments, graph results, nested
// signatures or nanosecond schedule intervals as Lua floating-point values.
type opaqueDocument struct {
	Format         int    `json:"format"`
	ID             string `json:"id"`
	SourceID       string `json:"source_id,omitempty"`
	Payload        []byte `json:"payload"`
	Digest         string `json:"digest,omitempty"`
	Revision       string `json:"revision"`
	Fence          string `json:"fence"`
	Owner          string `json:"owner"`
	LeaseMillis    int64  `json:"lease_millis"`
	ObservedMillis int64  `json:"observed_millis"`
	Delivered      bool   `json:"delivered"`
	Enabled        bool   `json:"enabled"`
}

// Increment decimal strings without converting complete fencing/revision
// counters to Lua numbers. This preserves all uint64 identities, not just 2^53.
const incrementCounterLua = `
local function incrementCounter(value)
 if type(value)~='string' or not string.match(value,'^%d+$') or (#value>1 and string.sub(value,1,1)=='0') or #value>20 or (#value==20 and value>='18446744073709551615') then error('GOGO_COUNTER_EXHAUSTED') end
 local result='';local carry=1
 for i=#value,1,-1 do
  local digit=tonumber(string.sub(value,i,i))+carry
  if digit==10 then digit=0;carry=1 else carry=0 end
  result=tostring(digit)..result
 end
 if carry==1 then result='1'..result end
 return result
end
`

func marshalDocument(document opaqueDocument, payload any) ([]byte, error) {
	var err error
	document.Format = 1
	if document.Fence == "" {
		document.Fence = "0"
	}
	if document.Revision == "" {
		document.Revision = "0"
	}
	document.Payload, err = json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(document)
}

func unmarshalDocument(raw []byte) (opaqueDocument, error) {
	var document opaqueDocument
	if err := json.Unmarshal(raw, &document); err != nil || document.Format != 1 || len(document.Payload) == 0 {
		return document, async.ErrUnavailable
	}
	for _, counter := range []string{document.Fence, document.Revision} {
		value, err := strconv.ParseUint(counter, 10, 64)
		if err != nil || strconv.FormatUint(value, 10) != counter {
			return document, async.ErrUnavailable
		}
	}
	return document, nil
}

func marshalIntents(intents []async.Intent) ([]byte, error) {
	documents := make([]opaqueDocument, 0, len(intents))
	for _, intent := range intents {
		payload, err := json.Marshal(intent)
		if err != nil {
			return nil, err
		}
		documents = append(documents, opaqueDocument{Format: 1, ID: intent.ID, SourceID: intent.SourceID, Payload: payload, Revision: "0", Fence: strconv.FormatUint(intent.Fence, 10), Owner: intent.Owner, Delivered: intent.Delivered})
	}
	return json.Marshal(documents)
}

func unmarshalIntent(raw []byte) (async.Intent, error) {
	document, err := unmarshalDocument(raw)
	if err != nil {
		return async.Intent{}, err
	}
	var intent async.Intent
	if err := json.Unmarshal(document.Payload, &intent); err != nil || intent.ID != document.ID || intent.SourceID != document.SourceID {
		return intent, async.ErrUnavailable
	}
	intent.Owner, intent.Delivered = document.Owner, document.Delivered
	intent.Fence, _ = strconv.ParseUint(document.Fence, 10, 64)
	if document.LeaseMillis > 0 {
		intent.LeaseUntil = time.UnixMilli(document.LeaseMillis)
	} else {
		intent.LeaseUntil = time.Time{}
	}
	return intent, nil
}

func marshalPeriodic(p async.PeriodicSchedule) ([]byte, error) {
	return marshalDocument(opaqueDocument{ID: p.ID, Revision: strconv.FormatUint(p.Revision, 10), Fence: strconv.FormatUint(p.Fence, 10), Owner: p.Owner, Enabled: p.Enabled}, p)
}

func unmarshalPeriodic(raw []byte) (async.PeriodicSchedule, error) {
	document, err := unmarshalDocument(raw)
	if err != nil {
		return async.PeriodicSchedule{}, err
	}
	var schedule async.PeriodicSchedule
	if err := json.Unmarshal(document.Payload, &schedule); err != nil || schedule.ID != document.ID {
		return schedule, async.ErrUnavailable
	}
	schedule.Revision, _ = strconv.ParseUint(document.Revision, 10, 64)
	schedule.Fence, _ = strconv.ParseUint(document.Fence, 10, 64)
	schedule.Owner, schedule.Enabled = document.Owner, document.Enabled
	if document.LeaseMillis > 0 {
		schedule.LeaseUntil = time.UnixMilli(document.LeaseMillis)
	} else {
		schedule.LeaseUntil = time.Time{}
	}
	if document.ObservedMillis > 0 {
		schedule.ObservedAt = time.UnixMilli(document.ObservedMillis)
	} else {
		schedule.ObservedAt = time.Time{}
	}
	return schedule, nil
}
