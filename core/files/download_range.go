package files

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// These parsers only consume the bounded, detached request snapshot. They do
// not inspect storage or invoke application callbacks.
type downloadConditions struct {
	match, noneMatch               []string
	matchPresent, noneMatchPresent bool
	ifRangePresent, rangePresent   bool
	unmodified, modified, ifRange  string
	rangeValue                     string
}

func downloadConditionShape(header http.Header) (downloadConditions, bool) {
	var c downloadConditions
	c.match, c.matchPresent = header["If-Match"]
	c.noneMatch, c.noneMatchPresent = header["If-None-Match"]
	_, c.ifRangePresent = header["If-Range"]
	_, c.rangePresent = header["Range"]
	for _, lines := range [][]string{c.match, c.noneMatch} {
		if len(lines) > 32 {
			return c, false
		}
		bytes := 0
		for _, line := range lines {
			if len(line) > 4096-bytes {
				return c, false
			}
			bytes += len(line)
		}
	}
	for name, target := range map[string]*string{
		"If-Unmodified-Since": &c.unmodified, "If-Modified-Since": &c.modified,
		"If-Range": &c.ifRange, "Range": &c.rangeValue,
	} {
		values := header[name]
		if len(values) > 1 || len(values) == 1 && len(values[0]) > 128 {
			return c, false
		}
		if len(values) == 1 {
			*target = values[0]
		}
	}
	return c, true
}

// downloadETags validates the whole list before reporting a match. In
// particular a matching prefix cannot conceal an invalid or overlong suffix.
func downloadETags(lines []string, current string, weak bool) (matched, valid bool) {
	if len(lines) == 0 || len(lines) > 32 {
		return false, false
	}
	bytes := len(lines) - 1
	for _, line := range lines {
		if len(line) > 4096-bytes {
			return false, false
		}
		bytes += len(line)
	}
	value := strings.Trim(strings.Join(lines, ","), " \t")
	if value == "*" {
		return true, true
	}
	positions := 0
	for value != "" {
		positions++
		if positions > 64 {
			return false, false
		}
		// RFC list recipients ignore a reasonable number of empty elements.
		// Empty positions still consume work, so commas cannot bypass the cap.
		if value[0] == ',' {
			value = strings.TrimLeft(value[1:], " \t")
			continue
		}
		isWeak := strings.HasPrefix(value, "W/")
		if isWeak {
			value = value[2:]
		}
		if len(value) < 2 || value[0] != '"' {
			return false, false
		}
		end := 1
		for end < len(value) && value[end] != '"' {
			ch := value[end]
			if ch < 0x21 || ch == 0x7f {
				return false, false
			}
			end++
		}
		if end == len(value) {
			return false, false
		}
		matched = matched || (weak || !isWeak) && value[:end+1] == current
		value = strings.TrimLeft(value[end+1:], " \t")
		if value != "" {
			if value[0] != ',' {
				return false, false
			}
			value = strings.TrimLeft(value[1:], " \t")
		}
	}
	return matched, true
}

func downloadPrecondition(c downloadConditions, etag string, modified time.Time) int {
	if c.matchPresent {
		if match, valid := downloadETags(c.match, etag, false); !valid || !match {
			return http.StatusPreconditionFailed
		}
	} else if date, err := http.ParseTime(c.unmodified); err == nil && !modified.IsZero() && modified.After(date) {
		return http.StatusPreconditionFailed
	}
	if c.noneMatchPresent {
		if match, valid := downloadETags(c.noneMatch, etag, true); valid && match {
			return http.StatusNotModified
		}
	} else if date, err := http.ParseTime(c.modified); err == nil && !modified.IsZero() && !modified.After(date) {
		return http.StatusNotModified
	}
	return 0
}

func downloadIfRange(value, etag string, modified time.Time, strongDate bool) bool {
	value = strings.Trim(value, " \t")
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "W/") {
		return value == etag
	}
	date, err := http.ParseTime(value)
	return err == nil && strongDate && !modified.IsZero() && modified.Equal(date)
}

// downloadRange supports one byte interval. Unknown units and all multi-range
// requests are ignored, not misclassified as unsatisfiable single intervals.
// The returned status is 200, 206, or 416; no arithmetic can overflow int64.
func downloadRange(value string, size int64) (start, length int64, status int) {
	start, length, status = 0, size, http.StatusOK
	if value == "" {
		return
	}
	unit, spec, present := strings.Cut(value, "=")
	if !present {
		if strings.EqualFold(strings.TrimSpace(unit), "bytes") {
			status = http.StatusRequestedRangeNotSatisfiable
		}
		return
	}
	if !strings.EqualFold(strings.TrimSpace(unit), "bytes") || strings.Contains(spec, ",") {
		return
	}
	status = http.StatusRequestedRangeNotSatisfiable
	if len(value) > 128 || !strings.EqualFold(unit, "bytes") || size <= 0 {
		return
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok || a == "" && b == "" {
		return
	}
	decimal := func(s string) (int64, bool) {
		if s == "" {
			return 0, false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0, false
			}
		}
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil
	}
	if a == "" {
		n, valid := decimal(b)
		if !valid || n == 0 {
			return
		}
		if n > size {
			n = size
		}
		return size - n, n, http.StatusPartialContent
	}
	n, valid := decimal(a)
	if !valid || n >= size {
		return
	}
	end := size - 1
	if b != "" {
		last, valid := decimal(b)
		if !valid || last < n {
			return
		}
		if last < end {
			end = last
		}
	}
	return n, end - n + 1, http.StatusPartialContent
}

func downloadModified(finalized, origin time.Time) (modified time.Time, strong bool) {
	if finalized.IsZero() {
		return time.Time{}, false
	}
	strong = !finalized.After(origin)
	if !strong {
		finalized = origin
	}
	modified = detachedFileTime(finalized).Truncate(time.Second)
	if modified.Year() < 1601 || modified.Year() > 9999 {
		return time.Time{}, false
	}
	// A ready file identity has one immutable representation; replacing a link
	// creates a new identity. A non-clamped finalization date is therefore strong
	// at the origin, even when its fractional second is omitted on the wire.
	return modified, strong
}
