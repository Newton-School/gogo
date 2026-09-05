package async

import (
	"strings"
	"testing"
)

func TestLinuxProcStatGroupParser(t *testing.T) {
	state, parent, group, threads, err := parseProcessStat([]byte("123 (name ) with spaces)) Z 12 123" + strings.Repeat(" 0", 14) + " 2 0 0"))
	if err != nil || state != 'Z' || parent != 12 || group != 123 || threads != 2 {
		t.Fatal(state, parent, group, threads, err)
	}
	for _, input := range []string{"", "123 (x) Z", "123 (x) ZZ 1 1", "123 (x) Z bad 1", "123 (x) Z 1 -1", strings.Repeat("x", 8193)} {
		if _, _, _, _, err := parseProcessStat([]byte(input)); err == nil {
			t.Fatal("bad stat accepted")
		}
	}
}
