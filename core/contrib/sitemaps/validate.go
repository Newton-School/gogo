package sitemaps

import (
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/language"
)

// Input sizes are bounded work/storage estimates, not serialized XML lengths.
// The serializer must independently enforce the escaped document byte limit.
func entryInputSize(entry Entry) (int, error) {
	if len(entry.Alternates) > 64 || len(entry.Loc) > maxLocationBytes || len(entry.LastMod.Date) > 10 || len(entry.ChangeFreq) > 16 {
		return 0, ErrLimit
	}
	size := 128 + len(entry.Loc) + len(entry.LastMod.Date) + len(entry.ChangeFreq)
	for _, alternate := range entry.Alternates {
		if len(alternate.Language) > 128 || len(alternate.Loc) > maxLocationBytes {
			return 0, ErrLimit
		}
		size += 32 + len(alternate.Language) + len(alternate.Loc)
	}
	return size, nil
}

func indexInputSize(entry IndexEntry) (int, error) {
	if len(entry.Loc) > maxLocationBytes || len(entry.LastMod.Date) > 10 {
		return 0, ErrLimit
	}
	return 96 + len(entry.Loc) + len(entry.LastMod.Date), nil
}

func (p *urlPolicy) normalizeEntry(entry Entry) (Entry, error) {
	if _, err := entryInputSize(entry); err != nil {
		return Entry{}, err
	}
	if _, err := lastModValue(entry.LastMod); err != nil {
		return Entry{}, err
	}
	switch entry.ChangeFreq {
	case "", "always", "hourly", "daily", "weekly", "monthly", "yearly", "never":
	default:
		return Entry{}, ErrInvalid
	}
	if entry.Priority != nil && (math.IsNaN(*entry.Priority) || math.IsInf(*entry.Priority, 0) || *entry.Priority < 0 || *entry.Priority > 1) {
		return Entry{}, ErrInvalid
	}
	loc, err := p.location(entry.Loc, false)
	if err != nil {
		return Entry{}, err
	}
	// Normalize into an independent slice; callers never observe partial output.
	alternates := make([]Alternate, len(entry.Alternates))
	seen := make(map[string]bool, len(entry.Alternates))
	self := len(entry.Alternates) == 0
	for i, alternate := range entry.Alternates {
		label, err := alternateLanguage(alternate.Language)
		if err != nil || seen[label] {
			return Entry{}, ErrInvalid
		}
		location, err := p.location(alternate.Loc, true)
		if err != nil {
			return Entry{}, err
		}
		seen[label] = true
		self = self || location == loc
		alternates[i] = Alternate{Language: label, Loc: location}
	}
	if !self {
		return Entry{}, ErrInvalid
	}
	result := cloneEntry(entry)
	result.Loc = loc
	if entry.Alternates != nil {
		result.Alternates = alternates
	}
	return result, nil
}

func (p *urlPolicy) normalizeIndex(entry IndexEntry) (IndexEntry, error) {
	if _, err := indexInputSize(entry); err != nil {
		return IndexEntry{}, err
	}
	if _, err := lastModValue(entry.LastMod); err != nil {
		return IndexEntry{}, err
	}
	loc, err := p.location(entry.Loc, false)
	if err != nil {
		return IndexEntry{}, err
	}
	entry = cloneIndexEntry(entry)
	entry.Loc = loc
	return entry, nil
}

func alternateLanguage(value string) (string, error) {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) {
		return "", ErrInvalid
	}
	if strings.EqualFold(value, "x-default") {
		return "x-default", nil
	}
	tag, err := language.Parse(value)
	if err != nil || tag.IsRoot() {
		return "", ErrInvalid
	}
	label := tag.String()
	if len(label) > 128 {
		return "", ErrInvalid
	}
	return label, nil
}

func lastModValue(value LastModified) (string, error) {
	if len(value.Date) > 10 {
		return "", ErrLimit
	}
	if value.Date != "" {
		if len(value.Date) != 10 || !value.Time.IsZero() {
			return "", ErrInvalid
		}
		date, err := time.Parse("2006-01-02", value.Date)
		if err != nil || date.Year() < 1 || date.Format("2006-01-02") != value.Date {
			return "", ErrInvalid
		}
		return value.Date, nil
	}
	if value.Time.IsZero() {
		return "", nil
	}
	_, offset := value.Time.Zone()
	utc := value.Time.UTC()
	if value.Time.Year() < 1 || value.Time.Year() > 9999 || utc.Year() < 1 || utc.Year() > 9999 || offset <= -86400 || offset >= 86400 {
		return "", ErrInvalid
	}
	return utc.Format(time.RFC3339Nano), nil
}

func cloneLastModified(value LastModified) LastModified {
	// Normalize to the wire instant, dropping monotonic-only clock metadata.
	// Copy UTC even for an unset time: Location() exposes an assignable pointer.
	utc := value.Time.UTC()
	zone := *utc.Location()
	value.Time = utc.In(&zone)
	return value
}

func cloneEntry(entry Entry) Entry {
	entry.LastMod = cloneLastModified(entry.LastMod)
	if entry.Priority != nil {
		value := *entry.Priority
		entry.Priority = &value
	}
	if entry.Alternates != nil {
		entry.Alternates = append(make([]Alternate, 0, len(entry.Alternates)), entry.Alternates...)
	}
	return entry
}

func cloneIndexEntry(entry IndexEntry) IndexEntry {
	entry.LastMod = cloneLastModified(entry.LastMod)
	return entry
}
