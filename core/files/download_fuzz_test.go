package files

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func FuzzDownloadSingleRange(f *testing.F) {
	for _, input := range []string{"", "bytes=0-0", "BYTES=0-0", "Bytes=-2", "bytes=0-", "bytes=-0", "bytes=9-1", "bytes=0-1,3-4", "items=0-1", "bytes=9223372036854775807-", "bytes=-9223372036854775808", "bytes=", "bytes =0-1"} {
		for _, size := range []uint64{0, 1, 10, math.MaxInt64} {
			f.Add(input, size)
		}
	}
	f.Fuzz(func(t *testing.T, input string, rawSize uint64) {
		if len(input) > 1024 {
			t.Skip()
		}
		size := int64(rawSize & math.MaxInt64)
		start, length, status := downloadRange(input, size)
		switch status {
		case http.StatusOK:
			if start != 0 || length != size {
				t.Fatal("ignored range changed representation bounds")
			}
		case http.StatusPartialContent:
			if size == 0 || start < 0 || start >= size || length <= 0 || length > size-start {
				t.Fatal("partial interval escaped the representation", start, length, size)
			}
			canonical := fmt.Sprintf("bytes=%d-%d", start, start+length-1)
			a, n, code := downloadRange(canonical, size)
			if a != start || n != length || code != status {
				t.Fatal("partial interval cannot round trip")
			}
		case http.StatusRequestedRangeNotSatisfiable:
			if start != 0 || length != size {
				t.Fatal("rejection exposed a partial interval")
			}
		default:
			t.Fatal("unexpected range outcome", status)
		}
		if strings.HasPrefix(input, "bytes=") {
			a, n, code := downloadRange("BYTES="+input[len("bytes="):], size)
			if a != start || n != length || code != status {
				t.Fatal("range-unit case changed interpretation")
			}
		}
	})
}

func FuzzDownloadETagLists(f *testing.F) {
	const current = `"current"`
	for _, input := range []string{current, "W/" + current, "*", `"other", "current"`, `"current", invalid`, "*\n*", "", `""`, `"unterminated`, `w/"current"`, `"current",`, "," + current} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 8192 {
			t.Skip()
		}
		lines := strings.Split(input, "\n")
		original := append([]string(nil), lines...)
		strong, validStrong := downloadETags(lines, current, false)
		weak, validWeak := downloadETags(lines, current, true)
		if !reflect.DeepEqual(original, lines) {
			t.Fatal("parser changed caller data")
		}
		if validStrong != validWeak || validStrong && strong && !weak {
			t.Fatal("weak comparison is stricter than strong comparison")
		}
		if len(lines) > 32 || len(input)-len(lines)+1 > 4096 {
			if validStrong {
				t.Fatal("over-budget entity-tag list accepted")
			}
		}
		// A matching prefix must not conceal a malformed later field line.
		prefixed := append([]string{current}, lines...)
		prefixed = append(prefixed, "invalid")
		if _, valid := downloadETags(prefixed, current, true); valid {
			t.Fatal("invalid suffix concealed by matching prefix")
		}
	})
}

func FuzzDownloadRequestOwnership(f *testing.F) {
	for _, pair := range [][2]string{{"", ""}, {"bytes=0-1", `"current"`}, {"BYTES=-1", "*"}, {"bytes=0-1,3-4", `W/"current"`}, {"bytes=0-", "bad\r\nheader"}} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, rangeValue, noneMatch string) {
		if len(rangeValue)+len(noneMatch) > 16384 {
			t.Skip()
		}
		req := &http.Request{Method: "GET", URL: &url.URL{Path: "/download/"}, Header: http.Header{
			"Range": {rangeValue}, "If-None-Match": {noneMatch}, "Cookie": {"session=opaque"},
		}, Form: url.Values{"discarded": {"form"}}, Trailer: http.Header{"Discarded": {"trailer"}}}
		req.SetPathValue("discarded", "private match state")
		frozen, conditions, valid := freezeDownloadRequest(req)
		if !valid {
			return
		}
		if frozen.Body != http.NoBody || frozen.GetBody != nil || frozen.TLS != nil || frozen.Form != nil || frozen.PostForm != nil || frozen.MultipartForm != nil || frozen.Response != nil || frozen.Trailer != nil || frozen.PathValue("discarded") != "" {
			t.Fatal("discarded request state crossed the boundary")
		}
		req.Header["Range"][0] = "retargeted"
		req.Header["If-None-Match"][0] = "retargeted"
		req.URL.Path = "/retargeted/"
		if frozen.URL.Path != "/download/" || frozen.Header.Get("Range") != rangeValue || frozen.Header.Get("If-None-Match") != noneMatch || conditions.rangeValue != rangeValue || len(conditions.noneMatch) != 1 || conditions.noneMatch[0] != noneMatch {
			t.Fatal("original request retained a mutable alias")
		}
		callback := downloadRequestCopy(frozen)
		callback.Header["Range"][0] = "callback mutation"
		callback.URL.Path = "/callback/"
		if frozen.URL.Path != "/download/" || frozen.Header.Get("Range") != rangeValue || conditions.rangeValue != rangeValue {
			t.Fatal("callback request retained a mutable alias")
		}
	})
}

func FuzzDownloadModificationDate(f *testing.F) {
	for _, instant := range []int64{0, 1, -1, 1788825600, math.MaxInt32, math.MaxInt64, math.MinInt64} {
		f.Add(instant, int64(1788825600))
	}
	f.Fuzz(func(t *testing.T, fileSeconds, originSeconds int64) {
		// Exercise representable and out-of-HTTP-range dates without overflowing
		// time.Time's internal epoch arithmetic at int64's extreme seconds.
		fileSeconds %= 1 << 50
		originSeconds %= 1 << 50
		fileTime := time.Unix(fileSeconds, 500000000)
		origin := time.Unix(originSeconds, 250000000)
		modified, strong := downloadModified(fileTime, origin)
		if modified.IsZero() {
			if strong {
				t.Fatal("missing date marked strong")
			}
			return
		}
		if modified.After(origin) || modified.Nanosecond() != 0 || modified.Year() < 1601 || modified.Year() > 9999 || strong && fileTime.After(origin) {
			t.Fatal("invalid HTTP modification date")
		}
		parsed, err := http.ParseTime(modified.Format(http.TimeFormat))
		if err != nil || !parsed.Equal(modified) {
			t.Fatal("HTTP date cannot round trip")
		}
		if downloadIfRange(modified.Format(http.TimeFormat), `"current"`, modified, strong) != strong {
			t.Fatal("If-Range accepted a weak or rejected a strong exact date")
		}
	})
}
