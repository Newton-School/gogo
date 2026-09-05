package testing

import "testing"

func TestSupportedVersion(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   bool
	}{
		{"Redis server v=7.2.0 sha=fixture", true},
		{"Redis server v=8.0.1 sha=fixture", true},
		{"Redis server v=7.0.15 sha=fixture", false},
		{"Redis server v=6.2.16 sha=fixture", false},
		{"Redis server v=7.unknown sha=fixture", false},
		{"Redis server v=7 sha=fixture", false},
		{"", false},
	} {
		if got := supportedVersion(tc.output); got != tc.want {
			t.Errorf("supportedVersion(%q) = %v; want %v", tc.output, got, tc.want)
		}
	}
}
