package management

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReloadCandidateConfigurationStrictProjection(t *testing.T) {
	const valid = `{"GOGO_ENV":"test","GOGO_SHUTDOWN_GRACE":1000000000,"GOGO_HTTP_ADDR":"127.0.0.1:0","GOGO_STORAGE_ROOT":"private/uploads","GOGO_STATIC_ROOT":"collected","GOGO_NEW_CUSTOM":"accepted","GOGO_LIST":["one","two"]}`
	settings, err := parseReloadConfiguration([]byte(valid))
	if err != nil || settings.Duration("GOGO_SHUTDOWN_GRACE") != time.Second || settings.String("GOGO_STORAGE_ROOT") != "private/uploads" {
		t.Fatal("valid projection rejected", err)
	}
	if settings.Get("GOGO_NEW_CUSTOM") != nil {
		t.Fatal("custom configuration escaped private projection")
	}
	for _, bad := range []string{
		`[]`, `{}`, valid + `{}`, strings.Replace(valid, `"test"`, `"production"`, 1),
		strings.Replace(valid, `1000000000`, `"1s"`, 1), strings.Replace(valid, `1000000000`, `0`, 1), strings.Replace(valid, `1000000000`, `61000000000`, 1),
		strings.Replace(valid, `"GOGO_ENV":"test"`, `"GOGO_ENV":"test","\u0047OGO_ENV":"test"`, 1),
		strings.Replace(valid, `"private/uploads"`, `"\ud800"`, 1),
		strings.Replace(valid, `"private/uploads"`, `"\udc00"`, 1),
		strings.Replace(valid, `"accepted"`, `{"secret":1}`, 1),
		strings.Replace(valid, `["one","two"]`, `[["one"]]`, 1),
		strings.Repeat(" ", reloadConfigLimit) + valid,
	} {
		if _, err := parseReloadConfiguration([]byte(bad)); err == nil {
			t.Fatalf("invalid candidate projection accepted: %.120q", bad)
		}
	}
	if _, err := parseReloadConfiguration([]byte(strings.Replace(valid, `"private/uploads"`, `"emoji/\ud83d\ude00"`, 1))); err != nil {
		t.Fatal("valid surrogate pair rejected", err)
	}
}

func TestReloadParentDoesNotRejectFreshCustomSchema(t *testing.T) {
	dir := t.TempDir()
	reloadTestFile(t, dir, ".env", "GOGO_NEW_CUSTOM=present\nGOGO_ENV=test\nGOGO_SHUTDOWN_GRACE=1s\n")
	w := reloadTestWatcher(t, dir, nil, nil)
	err := checkReloadEnvironment(context.Background(), w)
	if err != nil {
		t.Fatal("old project schema vetoed new candidate field", err)
	}
	// A newly compiled Environment layer may override these literal values.
	// Runtime controls (including production refusal) belong to the validated
	// fresh projection, not the original supervisor's stale declaration.
	reloadTestFile(t, dir, ".env", "GOGO_SHUTDOWN_GRACE=overridden-in-new-code\n")
	if err := checkReloadEnvironment(context.Background(), w); err != nil {
		t.Fatal("old precedence vetoed fresh candidate environment", err)
	}
	reloadTestFile(t, dir, ".env", "not an environment entry\n")
	if err := checkReloadEnvironment(context.Background(), w); err == nil {
		t.Fatal("malformed environment syntax accepted")
	}
}

func FuzzReloadConfiguration(f *testing.F) {
	f.Add([]byte(`{"GOGO_ENV":"test","GOGO_SHUTDOWN_GRACE":1000000000}`))
	f.Add([]byte(`{"GOGO_ENV":"test","GOGO_ENV":"production","GOGO_SHUTDOWN_GRACE":1}`))
	f.Add([]byte(`{"GOGO_ENV":"\ud800","GOGO_SHUTDOWN_GRACE":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > reloadConfigLimit+1 {
			return
		}
		settings, err := parseReloadConfiguration(data)
		if err != nil {
			return
		}
		if !json.Valid(data) || settings.String("GOGO_ENV") == "production" {
			t.Fatal("invalid input accepted")
		}
		encoded, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		again, err := parseReloadConfiguration(encoded)
		if err != nil || again.Duration("GOGO_SHUTDOWN_GRACE") != settings.Duration("GOGO_SHUTDOWN_GRACE") {
			t.Fatal("accepted settings are not closed under encoding", err)
		}
	})
}
