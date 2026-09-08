package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/templates"
)

func TestGenericTemplateCombinedContextByteBudgetBeforeLoader(t *testing.T) {
	part := templateContextMaxBytes/2 - 1
	for _, dynamic := range []bool{false, true} {
		for _, overflow := range []bool{false, true} {
			name := "extra"
			if dynamic {
				name = "dynamic"
			}
			if overflow {
				name += "/overflow"
			} else {
				name += "/exact"
			}
			t.Run(name, func(t *testing.T) {
				second := part
				if overflow {
					second++
				}
				// Each producer fits independently. Their distinct keys make
				// the effective context exactly 8 MiB, or one byte too large.
				processor := templates.Context{"a": strings.Repeat("a", part)}
				explicit := templates.Context{"b": strings.Repeat("b", second)}
				genericAssertTemplateBudget(t, processor, explicit, dynamic, overflow,
					`{{ a|length }}|{{ b|length }}`, strconv.Itoa(part)+"|"+strconv.Itoa(second))
			})
		}
	}
}

func TestGenericTemplateCombinedContextBudgetCountsOnlyEffectiveOverrides(t *testing.T) {
	large := strings.Repeat("a", 6<<20)
	// Both inputs fit separately; counting discarded shared data in the final
	// effective context would reject this valid 6 MiB representation.
	processor := templates.Context{"shared": large, "keep": "value"}
	explicit := templates.Context{"shared": "winner", "explicit": large}
	genericAssertTemplateBudget(t, processor, explicit, false, false,
		`{{ shared }}|{{ keep }}|{{ explicit|length }}`, "winner|value|"+strconv.Itoa(len(large)))

	// Overrides do not permit a producer to cross its own bound before its
	// output can be inspected and detached safely.
	oversized := templates.Context{"shared": strings.Repeat("a", templateContextMaxBytes)}
	genericAssertTemplateBudget(t, oversized, templates.Context{"shared": "winner"}, false, true,
		`{{ shared }}`, "")
}

func TestGenericTemplateCombinedContextNodeBudgetBeforeLoader(t *testing.T) {
	// Canonical []any elements cost two visits: interface and scalar. Two map
	// entries cost seven additional visits in total (root, keys and slices).
	part := (templateContextMaxValues - 7) / 4
	for _, overflow := range []bool{false, true} {
		name, second := "within", part
		if overflow {
			name, second = "overflow", part+1
		}
		t.Run(name, func(t *testing.T) {
			processor := templates.Context{"a": make([]int, part)}
			explicit := templates.Context{"b": make([]int, second)}
			genericAssertTemplateBudget(t, processor, explicit, false, overflow,
				`{{ a|length }}|{{ b|length }}`, strconv.Itoa(part)+"|"+strconv.Itoa(second))
		})
	}
}

func genericAssertTemplateBudget(t *testing.T, processor, explicit templates.Context, dynamic, reject bool, source, want string) {
	t.Helper()
	grants, processors, loads := 0, 0, 0
	options := TemplateViewOptions{
		ReadViewOptions: ReadViewOptions{Authorize: func(*http.Request) error { grants++; return nil }},
		TemplateName:    "budget.html",
		Templates: templates.Config{
			Loaders: []templates.Loader{genericTemplateLocaleLoader(func(context.Context, string) (string, error) {
				loads++
				return source, nil
			})},
			Processors: []templates.Processor{func(context.Context) (templates.Context, error) {
				processors++
				return processor, nil
			}},
		},
	}
	if dynamic {
		options.Context = func(*http.Request) (templates.Context, error) { return explicit, nil }
	} else {
		options.ExtraContext = explicit
	}
	handler, err := NewTemplateView(options)
	if err != nil {
		t.Fatal("individually valid explicit context rejected at construction", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/budget", nil))
	if reject {
		if response.Code != 503 || grants != 1 || processors != 1 || loads != 0 || response.Body.String() != "Service Unavailable\n" {
			t.Fatal("context overflow reached loading or disclosed output", response.Code, grants, processors, loads, response.Body.String())
		}
	} else if response.Code != 200 || grants != 2 || processors != 1 || loads != 1 || response.Body.String() != want {
		t.Fatal("effective context within budget failed", response.Code, grants, processors, loads, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("missing private cache policy")
	}
}
