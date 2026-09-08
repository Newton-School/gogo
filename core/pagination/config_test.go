package pagination

import (
	"errors"
	"net/url"
	"testing"
)

func TestConfigurationReturnsNormalizedDetachedRuntimeValues(t *testing.T) {
	for _, test := range []struct {
		input, want Config
	}{
		{Config{}, Config{Mode: PageNumber, DefaultSize: 50, MaxSize: 200, MaxOffset: 1_000_000}},
		{Config{Mode: LimitOffset, DefaultSize: 7, MaxSize: 31, MaxOffset: 123}, Config{Mode: LimitOffset, DefaultSize: 7, MaxSize: 31, MaxOffset: 123}},
		{Config{Mode: CursorMode}, Config{Mode: CursorMode, DefaultSize: 50, MaxSize: 200, MaxOffset: 1_000_000}},
	} {
		paginator, err := New(test.input)
		if err != nil {
			t.Fatal(err)
		}
		config, err := paginator.Configuration()
		if err != nil || config != test.want || paginator.config != test.want {
			t.Fatal("configuration did not expose exact normalized runtime values", config, err)
		}
		config.Mode, config.DefaultSize, config.MaxSize, config.MaxOffset = "changed", 1, 1, 1
		again, err := paginator.Configuration()
		if err != nil || again != test.want || paginator.config != test.want {
			t.Fatal("returned configuration changed paginator", again, err)
		}
		page, err := paginator.Parse(url.Values{})
		if err != nil || page.Size != test.want.DefaultSize || page.Offset != 0 {
			t.Fatal("configuration disagreed with actual pagination", page, err)
		}
	}
}

func TestConfigurationRejectsNilZeroAndInvalidWithoutNormalizing(t *testing.T) {
	for _, paginator := range []*Paginator{
		nil, {},
		{config: Config{Mode: "unknown", DefaultSize: 1, MaxSize: 2, MaxOffset: 10}},
		{config: Config{Mode: PageNumber, DefaultSize: 1, MaxSize: 201, MaxOffset: 10}},
		{config: Config{Mode: PageNumber, DefaultSize: 3, MaxSize: 2, MaxOffset: 10}},
		{config: Config{Mode: PageNumber, DefaultSize: -1, MaxSize: 2, MaxOffset: 10}},
		{config: Config{Mode: PageNumber, DefaultSize: 1, MaxSize: 2, MaxOffset: -1}},
		{config: Config{Mode: PageNumber, DefaultSize: 1, MaxSize: 2, MaxOffset: int(^uint(0) >> 1)}},
		// These would receive defaults in New, but are not initialized paginators.
		{config: Config{Mode: PageNumber, DefaultSize: 1, MaxSize: 2}},
		{config: Config{DefaultSize: 1, MaxSize: 2, MaxOffset: 10}},
	} {
		before := Config{}
		if paginator != nil {
			before = paginator.config
		}
		config, err := paginator.Configuration()
		if !errors.Is(err, ErrInvalid) || config != (Config{}) {
			t.Fatal("invalid paginator exposed guessed configuration", config, err)
		}
		if paginator != nil && paginator.config != before {
			t.Fatal("metadata read initialized or changed invalid paginator")
		}
	}
}
