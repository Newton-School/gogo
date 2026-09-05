package redis

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

func resultKeySnapshot(t *testing.T, connection *connector.Connection, keys []string) []string {
	t.Helper()
	var snapshot []string
	for _, key := range keys {
		ctx := context.Background()
		kind, err := connection.Client().Type(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		var value any
		switch kind {
		case "hash":
			value, err = connection.Client().HGetAll(ctx, key).Result()
		case "zset":
			value, err = connection.Client().ZRangeWithScores(ctx, key, 0, -1).Result()
		case "string":
			value, err = connection.Client().Get(ctx, key).Result()
		case "set":
			var members []string
			members, err = connection.Client().SMembers(ctx, key).Result()
			sort.Strings(members)
			value = members
		case "none":
		default:
			t.Fatal("unsupported fixture key type", kind)
		}
		if err != nil {
			t.Fatal(err)
		}
		// DUMP byte order is not stable for Redis hash tables: reads may
		// incrementally rehash them even when logical contents do not change.
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, kind+":"+string(raw))
	}
	return snapshot
}

func assertResultKeysUnchanged(t *testing.T, connection *connector.Connection, keys, before []string) {
	t.Helper()
	if after := resultKeySnapshot(t, connection, keys); !reflect.DeepEqual(before, after) {
		t.Fatal("failed operation partially changed task records, intents or inventory")
	}
}

func rewriteCleanupRecord(t *testing.T, results *Results, record async.Record) {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := results.Connection.Client().HSet(context.Background(), results.keys(record.Envelope.ID)[0], "record", raw, "revision", strconv.FormatUint(record.Revision, 10)).Err(); err != nil {
		t.Fatal(err)
	}
}

func terminalCleanupRecord(t *testing.T, results *Results, envelope async.Envelope, intents []async.Intent) async.Record {
	t.Helper()
	ctx := context.Background()
	claim, err := results.Claim(ctx, envelope, "worker", time.Minute)
	if err != nil || !claim.Acquired {
		t.Fatal(claim.Acquired, err)
	}
	if err := results.Transition(ctx, async.Transition{ID: envelope.ID, Fence: claim.Record.Fence, Owner: "worker", State: async.Succeeded, Output: json.RawMessage(preciseJSON), Intents: intents}); err != nil {
		t.Fatal(err)
	}
	record, err := results.read(ctx, envelope.ID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestRealRedisResultCleanupUsesStrictLexicographicBatches(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	results := &Results{Connection: connection}
	const partition = 19
	var ids []string
	for n := 0; len(ids) < 9; n++ {
		id := async.StableID("cleanup-batch", strconv.Itoa(n))
		if connector.Partition(id) == partition {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for i, id := range ids {
		e := codecEnvelope(t)
		e.ID = id
		if i%3 == 0 {
			if err := results.Register(ctx, e, async.Queued); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if i%3 == 1 {
			e.WorkflowID = async.StableID("cleanup-batch", "pinned")
		}
		record := terminalCleanupRecord(t, results, e, nil)
		record.PayloadExpiresAt = time.Now().Add(-time.Hour)
		rewriteCleanupRecord(t, results, record)
	}
	cursor := Cursor{Partition: partition}
	visited, cleared, pinned := 0, 0, 0
	for calls := 0; cursor.Partition == partition; calls++ {
		if calls > 5 {
			t.Fatal("cursor did not leave partition")
		}
		next, report, err := results.Cleanup(ctx, cursor, 2)
		if err != nil || report.Visited > 2 || report.Visited == 0 {
			t.Fatal(next, report, err)
		}
		visited += report.Visited
		cleared += report.PayloadsRemoved
		pinned += report.Pinned
		if next.Partition == partition && (next.AfterID <= cursor.AfterID || next.AfterID != ids[visited-1]) {
			t.Fatal("cursor is not a strict continuation", cursor, next)
		}
		cursor = next
	}
	if visited != 9 || cleared != 3 || pinned != 6 || cursor.AfterID != "" {
		t.Fatal(visited, cleared, pinned, cursor)
	}
	for i, id := range ids {
		record, err := results.read(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if i%3 == 1 && string(record.Output) != preciseJSON || i%3 == 2 && len(record.Output) != 0 {
			t.Fatal("cleanup lost a workflow pin or skipped a later payload")
		}
	}
	if next, report, err := results.Cleanup(ctx, Cursor{Partition: 63}, 2); err != nil || next != (Cursor{}) || report.Visited != 0 {
		t.Fatal(next, report, err)
	}
}

func TestRealRedisResultCleanupRejectsLegacyAndMalformedInventory(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	results := &Results{Connection: connection}
	e := codecEnvelope(t)
	if err := results.Register(ctx, e, async.Queued); err != nil {
		t.Fatal(err)
	}
	partition := connector.Partition(e.ID)
	for _, cursor := range []Cursor{{Partition: -1}, {Partition: 64}, {Partition: partition, Position: 1}, {Partition: partition, AfterID: "(arbitrary"}, {Partition: (partition + 1) % 64, AfterID: e.ID}} {
		if _, _, err := results.Cleanup(ctx, cursor, 1); !errors.Is(err, async.ErrInvalid) {
			t.Fatal(cursor, err)
		}
	}
	legacy, _ := connection.PartitionIndex("task", partition, "records")
	if err := connection.Client().SAdd(ctx, legacy, e.ID).Err(); err != nil {
		t.Fatal(err)
	}
	keys := append(results.keys(e.ID), legacy)
	before := resultKeySnapshot(t, connection, keys)
	cursor := Cursor{Partition: partition}
	if next, _, err := results.Cleanup(ctx, cursor, 1); err == nil || !strings.Contains(err.Error(), "migration") || next != cursor {
		t.Fatal(next, err)
	}
	assertResultKeysUnchanged(t, connection, keys, before)
	if err := connection.Client().Del(ctx, legacy).Err(); err != nil {
		t.Fatal(err)
	}
	if err := connection.Client().ZAdd(ctx, results.keys(e.ID)[3], redigo.Z{Member: "!invalid", Score: 0}).Err(); err != nil {
		t.Fatal(err)
	}
	before = resultKeySnapshot(t, connection, keys)
	if next, report, err := results.Cleanup(ctx, cursor, 2); !errors.Is(err, async.ErrUnavailable) || next != cursor || report.Visited != 0 {
		t.Fatal(next, report, err)
	}
	assertResultKeysUnchanged(t, connection, keys, before)
}

func TestRealRedisResultCASPreflightsEveryIntentBeforeWrites(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	results := &Results{Connection: connection}
	for _, operation := range []string{"create", "update"} {
		for _, badKind := range []string{"source_empty", "source_wrong", "id_invalid", "id_duplicate"} {
			t.Run(operation+"/"+badKind, func(t *testing.T) {
				e := codecEnvelope(t)
				record, err := async.InitialRecord(e, async.Queued)
				if err != nil {
					t.Fatal(err)
				}
				expected := uint64(0)
				if operation == "update" {
					if err := results.Register(ctx, e, async.Queued); err != nil {
						t.Fatal(err)
					}
					expected, record.Revision = 1, 2
				}
				good := async.Intent{ID: async.StableID(e.ID, "good"), SourceID: e.ID, Kind: "unpin"}
				bad := async.Intent{ID: async.StableID(e.ID, "bad"), SourceID: e.ID, Kind: "unpin"}
				switch badKind {
				case "source_empty":
					bad.SourceID = ""
				case "source_wrong":
					bad.SourceID = async.StableID(e.ID, "other")
				case "id_invalid":
					bad.ID = "invalid"
				case "id_duplicate":
					bad.ID = good.ID
				}
				keys := results.keys(e.ID)
				before := resultKeySnapshot(t, connection, keys)
				intents := []async.Intent{good, bad}
				if err := results.cas(ctx, expected, record, "create", 0, "", 0, intents); !errors.Is(err, async.ErrInvalid) {
					t.Fatal(err)
				}
				assertResultKeysUnchanged(t, connection, keys, before)
				// Bypass Go preflight to independently exercise Lua's staging.
				ib, _ := marshalIntents(intents)
				raw, _ := json.Marshal(record)
				if _, err := connection.Atomic(ctx, recordCAS, keys, strconv.FormatUint(expected, 10), raw, strconv.FormatUint(record.Revision, 10), 0, 0, "create", "", "0", "", "0", ib, e.ID); err == nil {
					t.Fatal("Lua accepted malformed later intent")
				}
				assertResultKeysUnchanged(t, connection, keys, before)
			})
		}
	}
}

func TestRealRedisResultScriptsPreflightAllKeyTypes(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	results := &Results{Connection: connection}
	for _, operation := range []string{"create", "update", "payload", "delete"} {
		for keyIndex := 0; keyIndex < 4; keyIndex++ {
			t.Run(operation+"/"+strconv.Itoa(keyIndex), func(t *testing.T) {
				e := codecEnvelope(t)
				record, _ := async.InitialRecord(e, async.Queued)
				expected := uint64(0)
				if operation != "create" {
					if err := results.Register(ctx, e, async.Queued); err != nil {
						t.Fatal(err)
					}
					expected, record.Revision = 1, 2
				}
				keys := results.keys(e.ID)
				// Only fixture-owned exact keys are changed. Preserve partition
				// indexes from earlier subtests and restore them afterward.
				prior, err := connection.Client().Dump(ctx, keys[keyIndex]).Result()
				if err != nil && !errors.Is(err, redigo.Nil) {
					t.Fatal(err)
				}
				if err := connection.Client().Set(ctx, keys[keyIndex], "wrong-type", 0).Err(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if prior == "" {
						_ = connection.Client().Del(ctx, keys[keyIndex]).Err()
					} else {
						_ = connection.Client().RestoreReplace(ctx, keys[keyIndex], 0, prior).Err()
					}
				})
				before := resultKeySnapshot(t, connection, keys)
				if operation == "create" || operation == "update" {
					err = results.cas(ctx, expected, record, "create", 0, "", 0, nil)
				} else {
					raw, _ := json.Marshal(record)
					_, err = connection.Atomic(ctx, cleanupScript, keys, "1", operation, raw, e.ID, "2")
				}
				if err == nil {
					t.Fatal("wrong key type accepted")
				}
				assertResultKeysUnchanged(t, connection, keys, before)
			})
		}
	}
}

func TestRealRedisResultCleanupRetainsPendingAndCorruptIntents(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	results := &Results{Connection: connection}
	for _, mode := range []string{"pending", "wrong_source", "wrong_field", "nonboolean_delivery", "legacy", "delivered"} {
		t.Run(mode, func(t *testing.T) {
			e := codecEnvelope(t)
			intent := async.Intent{ID: async.StableID(e.ID, "completion"), SourceID: e.ID, Kind: "completion"}
			record := terminalCleanupRecord(t, results, e, []async.Intent{intent})
			record.PayloadExpiresAt, record.TombstoneUntil = time.Now().Add(-time.Hour), time.Now().Add(-time.Minute)
			rewriteCleanupRecord(t, results, record)
			key := results.keys(e.ID)[1]
			document := readDocument(t, connection, key, intent.ID)
			document.Delivered = mode != "pending"
			switch mode {
			case "wrong_source":
				document.SourceID = async.StableID(e.ID, "wrong")
			case "wrong_field":
				document.ID = async.StableID(e.ID, "wrong")
			case "legacy":
				document.Format = 0
			}
			writeDocument(t, connection, key, intent.ID, document)
			if mode == "nonboolean_delivery" {
				raw, _ := json.Marshal(document)
				if err := connection.Client().HSet(ctx, key, intent.ID, strings.Replace(string(raw), `"delivered":true`, `"delivered":"yes"`, 1)).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "delivered" {
				if err := connection.Client().ZRem(ctx, results.keys(e.ID)[2], e.ID+":"+intent.ID).Err(); err != nil {
					t.Fatal(err)
				}
			}
			keys := results.keys(e.ID)
			before := resultKeySnapshot(t, connection, keys)
			out, err := connection.Atomic(ctx, cleanupScript, keys, strconv.FormatUint(record.Revision, 10), "delete", "", e.ID, "0")
			switch mode {
			case "pending":
				if err != nil || out != "PINNED" {
					t.Fatal(out, err)
				}
			case "delivered":
				if err != nil || out != "DELETED" {
					t.Fatal(out, err)
				}
				if _, err := results.read(ctx, e.ID); !errors.Is(err, async.ErrNotFound) {
					t.Fatal(err)
				}
				if _, err := connection.Client().ZScore(ctx, keys[3], e.ID).Result(); !errors.Is(err, redigo.Nil) {
					t.Fatal("deleted tombstone remains in task inventory", err)
				}
				return
			default:
				if err == nil {
					t.Fatal("corrupt intent permitted tombstone deletion")
				}
			}
			assertResultKeysUnchanged(t, connection, keys, before)
		})
	}
}

func TestRealRedisResultCleanupPreservesExactRevisionAndEnvelope(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	results := &Results{Connection: connection}
	e := codecEnvelope(t)
	record := terminalCleanupRecord(t, results, e, nil)
	record.Revision, record.Fence = highCounter+1, highCounter+1
	record.PayloadExpiresAt = time.Now().Add(-time.Hour)
	rewriteCleanupRecord(t, results, record)
	_, report, err := results.Cleanup(ctx, Cursor{Partition: connector.Partition(e.ID)}, 1)
	if err != nil || report.PayloadsRemoved != 1 {
		t.Fatal(report, err)
	}
	loaded, err := results.read(ctx, e.ID)
	if err != nil || loaded.Revision != highCounter+2 || loaded.Fence != highCounter+1 || string(loaded.Envelope.Args) != preciseJSON || loaded.Digest != e.Digest() || len(loaded.Output) != 0 {
		t.Fatal("cleanup changed exact metadata or opaque input", err)
	}
}

func TestRealRedisResultCleanupRejectsCorruptRecordWithoutMutation(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	results := &Results{Connection: connection}
	for partition, corruption := range []string{"id", "digest", "unknown_state", "missing_expiry", "missing_tombstone", "invalid_expiry", "counter_exhausted", "missing_record", "revision_mismatch", "noncanonical_revision"} {
		t.Run(corruption, func(t *testing.T) {
			e := codecEnvelope(t)
			for n := 0; ; n++ {
				e.ID = async.StableID("cleanup-corrupt-"+corruption, strconv.Itoa(n))
				if connector.Partition(e.ID) == partition {
					break
				}
			}
			record := terminalCleanupRecord(t, results, e, nil)
			record.PayloadExpiresAt = time.Now().Add(-time.Hour)
			switch corruption {
			case "id":
				record.Envelope.ID = async.StableID(e.ID, "corrupt")
			case "digest":
				record.Digest = "mismatch"
			case "unknown_state":
				record.State = async.Unknown
			case "missing_expiry":
				record.PayloadExpiresAt = time.Time{}
			case "missing_tombstone":
				record.TombstoneUntil = time.Time{}
			case "invalid_expiry":
				record.PayloadExpiresAt = record.TombstoneUntil.Add(time.Hour)
			case "counter_exhausted":
				record.Revision = math.MaxUint64
			}
			raw, _ := json.Marshal(record)
			if err := connection.Client().HSet(ctx, results.keys(e.ID)[0], "record", raw, "revision", strconv.FormatUint(record.Revision, 10)).Err(); err != nil {
				t.Fatal(err)
			}
			if corruption == "revision_mismatch" || corruption == "noncanonical_revision" {
				revision := strconv.FormatUint(record.Revision+1, 10)
				if corruption == "noncanonical_revision" {
					revision = "0" + strconv.FormatUint(record.Revision, 10)
				}
				if err := connection.Client().HSet(ctx, results.keys(e.ID)[0], "revision", revision).Err(); err != nil {
					t.Fatal(err)
				}
				if _, err := results.Lookup(ctx, e.ID); !errors.Is(err, async.ErrUnavailable) {
					t.Fatal("divergent revision was not surfaced", err)
				}
			}
			if corruption == "missing_record" {
				if err := connection.Client().Del(ctx, results.keys(e.ID)[0]).Err(); err != nil {
					t.Fatal(err)
				}
			}
			keys := results.keys(e.ID)
			before := resultKeySnapshot(t, connection, keys)
			cursor := Cursor{Partition: connector.Partition(e.ID)}
			next, report, err := results.Cleanup(ctx, cursor, 1)
			if err == nil || next != cursor || report.PayloadsRemoved != 0 || report.TombstonesRemoved != 0 {
				t.Fatal("corrupt authoritative record was cleaned", next, report, err)
			}
			assertResultKeysUnchanged(t, connection, keys, before)
		})
	}
}

func TestRealRedisResultCleanupConcurrentCASAndMalformedTransition(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	results := &Results{Connection: connection}
	e := codecEnvelope(t)
	claim, err := results.Claim(ctx, e, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	before := resultKeySnapshot(t, connection, results.keys(e.ID))
	transition := async.Transition{ID: e.ID, Fence: claim.Record.Fence, Owner: "worker", State: async.Succeeded, Output: json.RawMessage(preciseJSON), Intents: []async.Intent{{ID: async.StableID(e.ID, "one"), SourceID: e.ID}, {ID: async.StableID(e.ID, "bad")}}}
	if err := results.Transition(ctx, transition); !errors.Is(err, async.ErrInvalid) {
		t.Fatal(err)
	}
	assertResultKeysUnchanged(t, connection, results.keys(e.ID), before)
	transition.Intents = nil
	if err := results.Transition(ctx, transition); err != nil {
		t.Fatal(err)
	}
	record, err := results.read(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.PayloadExpiresAt = time.Now().Add(-time.Hour)
	rewriteCleanupRecord(t, results, record)
	var group sync.WaitGroup
	var mu sync.Mutex
	removed := 0
	for range 8 {
		group.Go(func() {
			_, report, err := results.Cleanup(ctx, Cursor{Partition: connector.Partition(e.ID)}, 1)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			removed += report.PayloadsRemoved
			mu.Unlock()
		})
	}
	group.Wait()
	if removed != 1 {
		t.Fatal("concurrent cleanup changed one payload more than once", removed)
	}
	if duplicate, err := results.Claim(ctx, e, "replay", time.Minute); err != nil || !duplicate.Duplicate || duplicate.Acquired {
		t.Fatal("payload cleanup lost replay tombstone", duplicate.Duplicate, err)
	}
}
