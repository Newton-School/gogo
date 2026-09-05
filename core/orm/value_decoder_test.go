package orm

import (
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type valueDecodingDialect struct {
	updateDialect
	calls *int
	fail  bool
}

func (d valueDecodingDialect) DecodeFieldValue(models.Field, any) (any, error) {
	(*d.calls)++
	if d.fail {
		return nil, errors.New("synthetic decode error")
	}
	return time.Second, nil
}

type decodingBackend struct {
	db.Backend
	dialect db.Dialect
}

func (b decodingBackend) Dialect() db.Dialect { return b.dialect }

type originalValueCodec struct{ received any }

func (c *originalValueCodec) Encode(v any) (any, error) { return v, nil }
func (c *originalValueCodec) Decode(v any) (any, error) { c.received = v; return time.Minute, nil }

func TestFieldValueDecodingSurvivesBackendDecoratorsAndCustomCodecWins(t *testing.T) {
	calls := 0
	dialect := valueDecodingDialect{calls: &calls}
	// Embedding only the required Backend contract does not lose an optional
	// conversion provided by the forwarded Dialect.
	backend := struct{ db.Backend }{decodingBackend{dialect: dialect}}
	store := New(backend, nil)
	field := models.DurationField("elapsed")
	if got, err := store.decodeField(field, "driver-specific"); err != nil || got != time.Second || calls != 1 {
		t.Fatal(got, err, calls)
	}
	codec := &originalValueCodec{}
	field.Codec = codec
	if got, err := store.decodeField(field, "original-driver"); err != nil || got != time.Minute || calls != 1 || codec.received != "original-driver" {
		t.Fatal("custom codec precedence lost", got, err, calls)
	}
	field.Codec = nil
	if got, err := store.decodeField(field, nil); err != nil || got != nil || calls != 1 {
		t.Fatal("SQLNULL called provider decoder", got, err, calls)
	}
	store.Backend = decodingBackend{dialect: valueDecodingDialect{calls: &calls, fail: true}}
	if _, err := store.decodeField(field, "invalid"); err == nil {
		t.Fatal("provider decode failure swallowed")
	}
}
