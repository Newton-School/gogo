package redis_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	connector "github.com/Newton-School/gogo/connectors/redis"
	fixture "github.com/Newton-School/gogo/connectors/redis/testing"
	"github.com/Newton-School/gogo/core/sessions"
)

func TestRealRedisCachePreservesBytesAndExactInt64(t *testing.T) {
	ctx := context.Background()
	cfg := fixture.Start(t)
	c, err := connector.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := &connector.Cache{Connection: c}
	payload := []byte("{\"z\":9007199254740993,\"a\":18446744073709551615}\n\x00\xff")
	for _, add := range []bool{false, true} {
		key := strconv.FormatBool(add)
		if add {
			if ok, err := s.Add(ctx, key, payload, time.Minute); err != nil || !ok {
				t.Fatal(ok, err)
			}
		} else if err := s.Set(ctx, key, payload, time.Minute); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, key)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatal(got, err)
		}
	}
	for _, tc := range []struct{ start, delta, want int64 }{
		{9007199254740992, 1, 9007199254740993}, {-9007199254740992, -1, -9007199254740993}, {math.MaxInt64 - 1, 1, math.MaxInt64}, {math.MinInt64 + 1, -1, math.MinInt64},
	} {
		if err := s.Set(ctx, "counter", []byte(strconv.FormatInt(tc.start, 10)), time.Minute); err != nil {
			t.Fatal(err)
		}
		got, err := s.Increment(ctx, "counter", tc.delta)
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
		stored, err := s.Get(ctx, "counter")
		if err != nil || string(stored) != strconv.FormatInt(tc.want, 10) {
			t.Fatal(string(stored), err)
		}
	}
	if _, err := s.Increment(ctx, "counter", -1); err == nil {
		t.Fatal("integer overflow accepted")
	}
	stored, _ := s.Get(ctx, "counter")
	if string(stored) != strconv.FormatInt(math.MinInt64, 10) {
		t.Fatal("overflow changed value", string(stored))
	}
}

func TestRealRedisSessionPreservesOpaqueDataAndVersion(t *testing.T) {
	ctx := context.Background()
	cfg := fixture.Start(t)
	cfg.Role = connector.SessionRole
	c, err := connector.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := &connector.Sessions{Connection: c}
	id := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	data := json.RawMessage(`{"z":9007199254740993,"auth_version":18446744073709551615,"a":-9007199254740993}`)
	r := sessions.Record{ID: id, Data: map[string]json.RawMessage{"identity": data}, Version: 9007199254740993, ExpiresAt: time.Now().Add(time.Hour).UTC()}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		loaded, err := s.Load(ctx, id)
		if err != nil || loaded.ID != id || loaded.Version != r.Version || !bytes.Equal(loaded.Data["identity"], data) {
			t.Fatal(loaded, err)
		}
		key := c.Namespace() + ":session:{" + connector.Digest(id) + "}"
		raw, err := c.Client().HGet(ctx, key, "payload").Bytes()
		if err != nil || bytes.Contains(raw, []byte(id)) || !bytes.Contains(raw, data) {
			t.Fatal(string(raw), err)
		}
		if i == 0 {
			previous := r.Version
			r.Version++
			if err := s.Save(ctx, r, previous); err != nil {
				t.Fatal(err)
			}
		}
	}
}
