package ratelimit

import (
	"math"
	"testing"
	"time"
)

func TestRejectInvalidLimits(t *testing.T) {
	for _, l := range []Limit{{0, 1, time.Second}, {math.NaN(), 1, time.Second}, {1, 0, time.Second}, {1, 1, 0}} {
		if l.Validate(1) == nil {
			t.Fatal(l)
		}
	}
	if (Limit{1, 2, time.Second}).Validate(1) != nil {
		t.Fatal("valid limit rejected")
	}
}
