package redis

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
	connector "github.com/Newton-School/gogo/connectors/redis"
	redigo "github.com/redis/go-redis/v9"
)

func TestRealRedisIntentDeliveryPreflightPreventsPartialMutation(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	results := &Results{Connection: connection}
	for _, corruption := range []string{"pending_key_type", "missing_source"} {
		t.Run(corruption, func(t *testing.T) {
			e := codecEnvelope(t)
			intent := async.Intent{ID: async.StableID(e.ID, "intent"), SourceID: e.ID, Kind: "unpin"}
			record, err := async.InitialRecord(e, async.Queued)
			if err != nil {
				t.Fatal(err)
			}
			if err := results.cas(ctx, 0, record, "create", 0, "", 0, []async.Intent{intent}); err != nil {
				t.Fatal(err)
			}
			claimed, err := results.ClaimIntent(ctx, e.ID, intent.ID, "relay", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			keys := results.intentBackend().keys(e.ID)
			if corruption == "pending_key_type" {
				prior, err := connection.Client().Dump(ctx, keys[1]).Result()
				if err != nil {
					t.Fatal(err)
				}
				if err := connection.Client().Set(ctx, keys[1], "wrong-type", 0).Err(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = connection.Client().RestoreReplace(ctx, keys[1], 0, prior).Err() })
			} else {
				document := readDocument(t, connection, keys[0], intent.ID)
				document.SourceID = ""
				writeDocument(t, connection, keys[0], intent.ID, document)
			}
			before := resultKeySnapshot(t, connection, keys)
			if err := results.MarkIntentDelivered(ctx, e.ID, intent.ID, claimed.Fence, "relay"); err == nil {
				t.Fatal("corrupt intent delivery was accepted")
			}
			assertResultKeysUnchanged(t, connection, keys, before)
		})
	}
}

func seedPreflightIntent(t *testing.T, connection *connector.Connection, family string) (intentBackend, async.Intent) {
	t.Helper()
	e := codecEnvelope(t)
	intent := async.Intent{ID: async.StableID(e.ID, "intent"), SourceID: e.ID, Kind: "publish", Envelope: &e}
	backend := intentBackend{connection, family}
	keys := backend.keys(e.ID)
	raw, err := marshalDocument(opaqueDocument{ID: intent.ID, SourceID: e.ID}, intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Client().HSet(context.Background(), keys[0], intent.ID, raw).Err(); err != nil {
		t.Fatal(err)
	}
	if err := connection.Client().ZAdd(context.Background(), keys[1], redigo.Z{Member: e.ID + ":" + intent.ID}).Err(); err != nil {
		t.Fatal(err)
	}
	return backend, intent
}

func TestRealRedisIntentMetadataPreflightAllFamilies(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	for _, family := range []string{"task", "workflow", "schedule"} {
		for _, operation := range []string{"claim", "mark"} {
			for _, corruption := range []string{"id", "source", "format", "fence", "revision", "lease", "delivered", "payload_type", "unknown_nonfinite"} {
				t.Run(family+"/"+operation+"/"+corruption, func(t *testing.T) {
					backend, intent := seedPreflightIntent(t, connection, family)
					fence := uint64(0)
					if operation == "mark" {
						claimed, err := backend.claim(ctx, intent.SourceID, intent.ID, "relay", time.Minute)
						if err != nil {
							t.Fatal(err)
						}
						fence = claimed.Fence
					}
					keys := backend.keys(intent.SourceID)
					document := readDocument(t, connection, keys[0], intent.ID)
					switch corruption {
					case "id":
						document.ID = async.StableID(intent.ID, "other")
					case "source":
						document.SourceID = ""
					case "format":
						document.Format = 0
					case "fence":
						document.Fence = "01"
					case "revision":
						document.Revision = "18446744073709551616"
					case "lease":
						document.LeaseMillis = -1
					case "payload_type":
						document.Payload = nil
					}
					raw, _ := json.Marshal(document)
					if corruption == "delivered" {
						raw = []byte(strings.Replace(string(raw), `"delivered":false`, `"delivered":"yes"`, 1))
					}
					if corruption == "unknown_nonfinite" {
						// Some cjson versions accept a non-finite exponent at
						// decode but cannot encode it. Encoding must precede writes.
						raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"extension":1e999}`)
					}
					if err := connection.Client().HSet(ctx, keys[0], intent.ID, raw).Err(); err != nil {
						t.Fatal(err)
					}
					before := resultKeySnapshot(t, connection, keys)
					var err error
					if operation == "claim" {
						_, err = backend.claim(ctx, intent.SourceID, intent.ID, "relay", time.Minute)
					} else {
						err = backend.mark(ctx, intent.SourceID, intent.ID, fence, "relay")
					}
					if err == nil {
						t.Fatal("malformed metadata accepted")
					}
					assertResultKeysUnchanged(t, connection, keys, before)
				})
			}
		}
	}
}

func TestRealRedisIntentLookupRejectsMisroutedReferences(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	for _, corruption := range []string{"nonuuid", "wrong_partition", "different_document"} {
		t.Run(corruption, func(t *testing.T) {
			backend, intent := seedPreflightIntent(t, connection, "task")
			keys := backend.keys(intent.SourceID)
			index := keys[1]
			reference := intent.SourceID + ":" + intent.ID
			if err := connection.Client().ZRem(ctx, index, reference).Err(); err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "nonuuid":
				reference = "arbitrary:" + intent.ID
			case "wrong_partition":
				index, _ = connection.PartitionIndex("task", (connector.Partition(intent.SourceID)+1)%64, "pending-intents")
			case "different_document":
				other := intent
				other.ID = async.StableID(intent.ID, "different")
				raw, err := marshalDocument(opaqueDocument{ID: other.ID, SourceID: other.SourceID}, other)
				if err != nil {
					t.Fatal(err)
				}
				if err := connection.Client().HSet(ctx, keys[0], intent.ID, raw).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if err := connection.Client().ZAdd(ctx, index, redigo.Z{Member: reference}).Err(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Client().ZRem(ctx, index, reference).Err() })
			keys = append(keys, index)
			before := resultKeySnapshot(t, connection, keys)
			if items, err := backend.list(ctx, 100); !errors.Is(err, async.ErrUnavailable) || len(items) != 0 {
				t.Fatal("misrouted pending reference returned", len(items), err)
			}
			assertResultKeysUnchanged(t, connection, keys, before)
		})
	}
}

func TestRealRedisIntentPreflightPreservesExactFenceAndOpaquePayload(t *testing.T) {
	ctx := context.Background()
	connection := codecConnection(t)
	for _, family := range []string{"task", "workflow", "schedule"} {
		backend, intent := seedPreflightIntent(t, connection, family)
		keys := backend.keys(intent.SourceID)
		document := readDocument(t, connection, keys[0], intent.ID)
		originalPayload := string(document.Payload)
		document.Fence = strconv.FormatUint(highCounter+1, 10)
		writeDocument(t, connection, keys[0], intent.ID, document)
		claimed, err := backend.claim(ctx, intent.SourceID, intent.ID, "relay", time.Minute)
		if err != nil || claimed.Fence != highCounter+2 || string(claimed.Envelope.Args) != preciseJSON {
			t.Fatal("claim changed exact fence or payload", err)
		}
		before := resultKeySnapshot(t, connection, keys)
		if err := backend.mark(ctx, intent.SourceID, intent.ID, highCounter+1, "relay"); !errors.Is(err, async.ErrLeaseLost) {
			t.Fatal(err)
		}
		assertResultKeysUnchanged(t, connection, keys, before)
		if err := backend.mark(ctx, intent.SourceID, intent.ID, claimed.Fence, "relay"); err != nil {
			t.Fatal(err)
		}
		if err := backend.mark(ctx, intent.SourceID, intent.ID, claimed.Fence, "relay"); err != nil {
			t.Fatal("duplicate delivery acknowledgment failed", err)
		}
		final := readDocument(t, connection, keys[0], intent.ID)
		if !final.Delivered || final.Fence != strconv.FormatUint(highCounter+2, 10) || string(final.Payload) != originalPayload {
			t.Fatal("delivery changed exact metadata or opaque bytes")
		}
	}
}
