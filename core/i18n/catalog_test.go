package i18n_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/templates"
)

func translationResolver(t testing.TB) *i18n.Resolver {
	t.Helper()
	r, err := i18n.New(i18n.Config{Languages: []string{"en", "fr", "fr-CA", "ar", "pl", "ru", "zh-TW"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func languageContext(t testing.TB, resolver *i18n.Resolver, language string) context.Context {
	t.Helper()
	ctx, err := resolver.WithLocale(context.Background(), i18n.Preferences{Language: language})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestCatalogNamespacedContextLookupParentsAndFallbacks(t *testing.T) {
	r := translationResolver(t)
	translator, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: []i18n.Catalog{
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "Open", Text: "Open"}, {ID: "Fallback", Text: "English fallback"}}},
		{Language: "fr", Domain: "shop", Messages: []i18n.Translation{{ID: "Open", Text: "Ouvrir"}, {ID: "Open", Context: "state", Text: "Ouvert"}, {ID: "Hidden", Text: ""}}},
		{Language: "fr", Domain: "account", Messages: []i18n.Translation{{ID: "Open", Text: "Créer"}}},
		{Language: "fr-CA", Domain: "shop", Messages: []i18n.Translation{{ID: "Regional", Text: "Au Canada"}}},
		{Language: "zh-Hant", Domain: "shop", Messages: []i18n.Translation{{ID: "Open", Text: "開啟"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ language, domain, context, source, expected string }{
		{"fr", "shop", "", "Open", "Ouvrir"},
		{"fr-CA", "shop", "", "Open", "Ouvrir"},
		{"fr-CA", "shop", "", "Regional", "Au Canada"},
		{"fr-CA", "shop", "state", "Open", "Ouvert"},
		{"fr-CA", "shop", "verb", "Open", "Open"},
		{"fr", "account", "", "Open", "Créer"},
		{"fr", "shop", "", "Hidden", ""},
		{"ar", "shop", "", "Fallback", "English fallback"},
		{"ar", "other", "", "Fallback", "Fallback"},
		{"zh-TW", "shop", "", "Open", "開啟"},
	} {
		got, err := translator.Pgettext(languageContext(t, r, test.language), test.domain, test.context, test.source)
		if err != nil || got != test.expected {
			t.Fatal(test, got, err)
		}
	}
	disabled, err := i18n.NewTranslator(r, i18n.TranslatorConfig{FallbackLanguages: []string{}, Catalogs: []i18n.Catalog{{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "Open", Text: "English translation"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if text, err := disabled.Gettext(languageContext(t, r, "ar"), "shop", "Open"); err != nil || text != "Open" {
		t.Fatal("explicit empty fallback list ignored", text, err)
	}
	other, _ := i18n.New(i18n.Config{Languages: []string{"de"}})
	if text, err := translator.Gettext(languageContext(t, other, "de"), "shop", "Open"); !errors.Is(err, i18n.ErrInvalidLocale) || text != "" {
		t.Fatal("foreign context silently selected a different language", text, err)
	}
}

func TestPluralCategoriesUseExactCountsAndSupplyingCatalogLanguage(t *testing.T) {
	r := translationResolver(t)
	forms := map[i18n.PluralForm]string{i18n.Zero: "zero", i18n.One: "one", i18n.Two: "two", i18n.Few: "few", i18n.Many: "many", i18n.Other: "other"}
	var catalogs []i18n.Catalog
	for _, language := range []string{"en", "fr", "ar", "pl", "ru"} {
		catalogs = append(catalogs, i18n.Catalog{Language: language, Domain: "shop", Messages: []i18n.Translation{{ID: "item", Forms: forms}, {ID: "item", Context: "context", Forms: forms}}})
	}
	catalogs[0].Messages = append(catalogs[0].Messages, i18n.Translation{ID: "fallback item", Forms: map[i18n.PluralForm]string{i18n.One: "one fallback", i18n.Other: "other fallback"}})
	translator, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: catalogs})
	if err != nil {
		t.Fatal(err)
	}
	forms[i18n.One] = "mutated"
	catalogs[0].Messages[0].ID = "mutated"
	for _, test := range []struct {
		language string
		count    int64
		form     string
	}{
		{"en", 0, "other"}, {"en", 1, "one"}, {"en", -1, "one"}, {"en", 2, "other"},
		{"en", 10000001, "other"}, {"en", math.MinInt64, "other"},
		{"fr", 0, "one"}, {"fr", 1, "one"}, {"fr", 2, "other"},
		{"ar", 0, "zero"}, {"ar", 1, "one"}, {"ar", 2, "two"}, {"ar", 3, "few"}, {"ar", 11, "many"}, {"ar", 100, "other"},
		{"pl", 1, "one"}, {"pl", 2, "few"}, {"pl", 5, "many"}, {"pl", 12, "many"}, {"pl", 22, "few"},
		{"ru", 1, "one"}, {"ru", 2, "few"}, {"ru", 5, "many"}, {"ru", 21, "one"},
		{"ru", 9007199254741001, "one"}, {"ru", math.MaxInt64, "many"}, {"ru", math.MinInt64, "many"},
	} {
		ctx := languageContext(t, r, test.language)
		for _, messageContext := range []string{"", "context"} {
			got, err := translator.NPgettext(ctx, "shop", messageContext, "item", "items", test.count)
			if err != nil || got != test.form {
				t.Fatal(test, messageContext, got, err)
			}
		}
	}
	// French zero is One; the English fallback must use English Other.
	if text, err := translator.Ngettext(languageContext(t, r, "fr"), "shop", "fallback item", "fallback items", 0); err != nil || text != "other fallback" {
		t.Fatal("plural rules used requested instead of supplying language", text, err)
	}
	for _, count := range []int64{-1, 0, 1, 2, math.MaxInt64, math.MinInt64} {
		want := "missing items"
		if count == 1 {
			want = "missing item"
		}
		if text, err := translator.Ngettext(languageContext(t, r, "ar"), "shop", "missing item", "missing items", count); err != nil || text != want {
			t.Fatal("source fallback changed", count, text, err)
		}
	}
}

func TestCatalogRejectsInvalidAndAmbiguousDataAtConstruction(t *testing.T) {
	r := translationResolver(t)
	for _, test := range []i18n.Catalog{
		{Language: "de", Domain: "shop"}, {Language: "und", Domain: "shop"}, {Language: "en", Domain: "bad/domain"},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: ""}}},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "x\x00y"}}},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "bad", Text: "\xff"}}},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "item", Forms: map[i18n.PluralForm]string{i18n.One: "one"}}}},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "item", Forms: map[i18n.PluralForm]string{}}}},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "item", Forms: map[i18n.PluralForm]string{i18n.Other: "other", "unknown": "bad"}}}},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "item", Text: "ambiguous", Forms: map[i18n.PluralForm]string{i18n.Other: "other"}}}},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "item"}, {ID: "item"}}},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: strings.Repeat("a", 4097)}}},
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "x", Text: strings.Repeat("a", (64<<10)+1)}}},
	} {
		if _, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: []i18n.Catalog{test}}); !errors.Is(err, i18n.ErrInvalidCatalog) {
			t.Fatal("invalid catalog accepted", err)
		}
	}
	for _, config := range []i18n.TranslatorConfig{
		{FallbackLanguages: []string{"de"}}, {FallbackLanguages: []string{"en", "EN"}},
		{Catalogs: make([]i18n.Catalog, 4097)}, {FallbackLanguages: make([]string, 257)},
		{Catalogs: []i18n.Catalog{{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "x"}}}, {Language: "EN", Domain: "shop", Messages: []i18n.Translation{{ID: "x"}}}}},
		{Catalogs: []i18n.Catalog{{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "x"}}}, {Language: "fr", Domain: "shop", Messages: []i18n.Translation{{ID: "x", Forms: map[i18n.PluralForm]string{i18n.Other: "plural"}}}}}},
	} {
		if _, err := i18n.NewTranslator(r, config); !errors.Is(err, i18n.ErrInvalidCatalog) {
			t.Fatal("invalid constructor configuration accepted", err)
		}
	}
	if _, err := i18n.NewTranslator(nil, i18n.TranslatorConfig{}); !errors.Is(err, i18n.ErrInvalidCatalog) {
		t.Fatal(err)
	}
}

func TestCatalogAggregateBudgetsRejectWholeCompilation(t *testing.T) {
	r := translationResolver(t)
	for _, test := range []struct{ entries, textBytes int }{{100001, 0}, {1024, 64 << 10}} {
		messages := make([]i18n.Translation, test.entries)
		text := strings.Repeat("a", test.textBytes)
		for i := range messages {
			messages[i] = i18n.Translation{ID: strconv.Itoa(i), Text: text}
		}
		if translator, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: []i18n.Catalog{{Language: "en", Domain: "shop", Messages: messages}}}); !errors.Is(err, i18n.ErrInvalidCatalog) || translator != nil {
			t.Fatal("overbudget catalog partially compiled", test, err)
		}
	}
}

func TestTranslationIsPlainTextAndKindMismatchIsExplicit(t *testing.T) {
	r := translationResolver(t)
	unsafe := `<script>alert("x")</script> {{ .Secret }} %s ${macro}`
	translator, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: []i18n.Catalog{{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "plain", Text: unsafe}, {ID: "plural", Forms: map[i18n.PluralForm]string{i18n.Other: ""}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	text, err := translator.Gettext(context.Background(), "shop", "plain")
	if err != nil || text != unsafe {
		t.Fatal("translation executed or changed source data", text, err)
	}
	output, err := templates.New(templates.Config{}).RenderString(context.Background(), `<div>{{ message }}</div>`, templates.Context{"message": text})
	if err != nil || strings.Contains(output, "<script>") || !strings.Contains(output, "&lt;script&gt;") {
		t.Fatal("translation incorrectly promoted to safe HTML", output, err)
	}
	if _, err := translator.Gettext(context.Background(), "shop", "plural"); !errors.Is(err, i18n.ErrInvalidMessage) {
		t.Fatal("plural entry silently became singular", err)
	}
	if _, err := translator.Ngettext(context.Background(), "shop", "plain", "plains", 2); !errors.Is(err, i18n.ErrInvalidMessage) {
		t.Fatal("singular entry silently became plural", err)
	}
	if text, err := translator.Ngettext(context.Background(), "shop", "plural", "plurals", 1); err != nil || text != "" {
		t.Fatal("missing category ignored deliberate empty Other", text, err)
	}
	for _, call := range []func() (string, error){
		func() (string, error) { return translator.Gettext(nil, "shop", "plain") },
		func() (string, error) { return translator.Gettext(context.Background(), "bad/domain", "plain") },
		func() (string, error) { return translator.Gettext(context.Background(), "shop", "") },
		func() (string, error) { return translator.Ngettext(context.Background(), "shop", "plural", "", 2) },
		func() (string, error) { return (*i18n.Translator)(nil).Gettext(context.Background(), "shop", "x") },
	} {
		if text, err := call(); text != "" || !errors.Is(err, i18n.ErrInvalidMessage) {
			t.Fatal(text, err)
		}
	}
}

func TestMissingMessagesAreDevelopmentOnlyAndPreserveCancellation(t *testing.T) {
	r := translationResolver(t)
	for _, development := range []bool{false, true} {
		called := 0
		translator, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Development: development, Missing: func(ctx context.Context, missing i18n.MissingMessage) {
			called++
			if missing.Language != "en" || missing.Domain != "shop" || missing.ID != "source" || ctx == nil {
				t.Fatal(missing)
			}
			panic("private development observer detail")
		}})
		if err != nil {
			t.Fatal(err)
		}
		text, err := translator.Gettext(context.Background(), "shop", "source")
		if err != nil || text != "source" || (called == 1) != development {
			t.Fatal(text, err, called)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	translator, _ := i18n.NewTranslator(r, i18n.TranslatorConfig{Development: true, Missing: func(context.Context, i18n.MissingMessage) { cancel() }})
	if text, err := translator.Gettext(ctx, "shop", "source"); text != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("observer cancellation ignored", text, err)
	}
}

func TestLazyMessagesResolveIndependentlyAcrossConcurrentContexts(t *testing.T) {
	r := translationResolver(t)
	translator, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: []i18n.Catalog{
		{Language: "en", Domain: "shop", Messages: []i18n.Translation{{ID: "Hello", Text: "Hello"}, {ID: "item", Forms: map[i18n.PluralForm]string{i18n.One: "item", i18n.Other: "items"}}}},
		{Language: "fr", Domain: "shop", Messages: []i18n.Translation{{ID: "Hello", Text: "Bonjour"}, {ID: "item", Forms: map[i18n.PluralForm]string{i18n.One: "article", i18n.Other: "articles"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	lazy := i18n.LazyMessage{Domain: "shop", Singular: "Hello"}
	plurals := i18n.LazyMessage{Domain: "shop", Singular: "item", Plural: "items"}
	var workers sync.WaitGroup
	for i := 0; i < 200; i++ {
		workers.Go(func() {
			language, expected, plural := "en", "Hello", "items"
			if i%2 == 1 {
				language, expected, plural = "fr", "Bonjour", "articles"
			}
			ctx := languageContext(t, r, language)
			if text, err := lazy.Resolve(ctx, translator); err != nil || text != expected {
				t.Error(text, err)
			}
			if text, err := plurals.ResolveCount(ctx, translator, 2); err != nil || text != plural {
				t.Error(text, err)
			}
		})
	}
	workers.Wait()
	if _, err := plurals.Resolve(context.Background(), translator); !errors.Is(err, i18n.ErrInvalidMessage) {
		t.Fatal("plural lazy value resolved without count", err)
	}
	if _, err := lazy.ResolveCount(context.Background(), translator, 1); !errors.Is(err, i18n.ErrInvalidMessage) {
		t.Fatal("singular lazy value accepted count", err)
	}
}

func FuzzCatalogCompilationAndLookup(f *testing.F) {
	r := translationResolver(f)
	for _, seed := range []string{
		`{"language":"en","domain":"shop","messages":[{"id":"item","text":"text"}]}`,
		`{"language":"ar","domain":"shop","messages":[{"id":"item","forms":{"one":"one","other":"other"}}]}`,
		`{"language":"en","domain":"shop","messages":[{"id":"item","text":""}]}`,
	} {
		f.Add(seed, int64(1))
	}
	f.Fuzz(func(t *testing.T, source string, count int64) {
		if len(source) > 64<<10 {
			t.Skip()
		}
		var catalog i18n.Catalog
		if json.Unmarshal([]byte(source), &catalog) != nil {
			return
		}
		translator, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: []i18n.Catalog{catalog}})
		if err != nil {
			return
		}
		ctx := languageContext(t, r, "en")
		for _, entry := range catalog.Messages {
			if len(entry.Forms) == 0 {
				if _, err := translator.Pgettext(ctx, catalog.Domain, entry.Context, entry.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				text, err := translator.NPgettext(ctx, catalog.Domain, entry.Context, entry.ID, "fallback", count)
				values := []string{entry.ID, "fallback"}
				for _, value := range entry.Forms {
					values = append(values, value)
				}
				if err != nil || !slices.Contains(values, text) {
					t.Fatal("lookup invented output", text, err)
				}
			}
		}
	})
}
