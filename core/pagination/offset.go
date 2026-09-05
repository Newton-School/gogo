// Package pagination provides bounded, deterministic pagination contracts.
package pagination

import (
	"errors"
	"net/url"
	"strconv"
)

var ErrInvalid = errors.New("pagination: invalid pagination parameters")

type Mode string

const (
	PageNumber  Mode = "page"
	LimitOffset Mode = "offset"
	CursorMode  Mode = "cursor"
)

type Config struct {
	Mode                 Mode
	DefaultSize, MaxSize int
	// MaxOffset bounds expensive scans. Zero uses 1,000,000 rows. Offset
	// pagination can drift during concurrent changes; it is not a snapshot.
	MaxOffset int
}

type Paginator struct{ config Config }
type Page struct{ Size, Offset int }

func New(config Config) (*Paginator, error) {
	if config.Mode == "" {
		config.Mode = PageNumber
	}
	if config.DefaultSize == 0 {
		config.DefaultSize = 50
	}
	if config.MaxSize == 0 {
		config.MaxSize = 200
	}
	if config.MaxOffset == 0 {
		config.MaxOffset = 1_000_000
	}
	if config.Mode != PageNumber && config.Mode != LimitOffset && config.Mode != CursorMode || config.DefaultSize < 1 || config.MaxSize < config.DefaultSize || config.MaxSize > 200 || config.MaxOffset < 1 || config.MaxOffset > int(^uint(0)>>1)-201 {
		return nil, ErrInvalid
	}
	return &Paginator{config: config}, nil
}

// Parse ignores non-pagination parameters; the resource/filter owner must
// separately allowlist them. Mixed modes and repeated parameters are rejected.
func (p *Paginator) Parse(values url.Values) (Page, error) {
	if p == nil {
		return Page{}, ErrInvalid
	}
	result := Page{Size: p.config.DefaultSize}
	for _, key := range []string{"page", "page_size", "limit", "offset", "cursor"} {
		items, exists := values[key]
		if !exists {
			continue
		}
		if len(items) != 1 || key == "cursor" && (p.config.Mode != CursorMode || items[0] == "" || len(items[0]) > MaxCursorBytes) || p.config.Mode == PageNumber && (key == "limit" || key == "offset") || p.config.Mode == LimitOffset && (key == "page" || key == "page_size") || p.config.Mode == CursorMode && (key == "page" || key == "limit" || key == "offset") {
			return Page{}, ErrInvalid
		}
	}
	read := func(key string, fallback int) (int, error) {
		items, exists := values[key]
		if !exists {
			return fallback, nil
		}
		if len(items[0]) == 0 || len(items[0]) > 20 {
			return 0, ErrInvalid
		}
		for _, c := range items[0] {
			if c < '0' || c > '9' {
				return 0, ErrInvalid
			}
		}
		value, err := strconv.Atoi(items[0])
		if err != nil {
			return 0, ErrInvalid
		}
		return value, nil
	}
	var err error
	if p.config.Mode == PageNumber || p.config.Mode == CursorMode {
		result.Size, err = read("page_size", result.Size)
		if err != nil || result.Size < 1 || result.Size > p.config.MaxSize {
			return Page{}, ErrInvalid
		}
		if p.config.Mode == CursorMode {
			return result, nil
		}
		page, err := read("page", 1)
		if err != nil || page < 1 || page-1 > p.config.MaxOffset/result.Size {
			return Page{}, ErrInvalid
		}
		result.Offset = (page - 1) * result.Size
	} else {
		result.Size, err = read("limit", result.Size)
		if err != nil || result.Size < 1 || result.Size > p.config.MaxSize {
			return Page{}, ErrInvalid
		}
		result.Offset, err = read("offset", 0)
		if err != nil || result.Offset > p.config.MaxOffset {
			return Page{}, ErrInvalid
		}
	}
	return result, nil
}

// Links emits relative links, never a client-supplied Host. It preserves only
// the query already validated by the caller and does not mutate it.
func (p *Paginator) Links(values url.Values, page Page, hasMore bool) (next, previous string) {
	if p == nil || p.config.Mode == CursorMode || page.Size < 1 || page.Size > p.config.MaxSize || page.Offset < 0 || page.Offset > p.config.MaxOffset {
		return "", ""
	}
	link := func(offset int) string {
		copy := make(url.Values, len(values)+2)
		for key, items := range values {
			copy[key] = append([]string(nil), items...)
		}
		if p.config.Mode == PageNumber {
			copy.Set("page", strconv.Itoa(offset/page.Size+1))
			copy.Set("page_size", strconv.Itoa(page.Size))
		} else {
			copy.Set("offset", strconv.Itoa(offset))
			copy.Set("limit", strconv.Itoa(page.Size))
		}
		return "?" + copy.Encode()
	}
	if hasMore && page.Offset <= p.config.MaxOffset-page.Size {
		next = link(page.Offset + page.Size)
	}
	if page.Offset > 0 {
		previous = link(max(0, page.Offset-page.Size))
	}
	return
}
