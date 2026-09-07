package i18n

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/language"
)

var ErrInvalidCatalog = errors.New("i18n: invalid translation catalog")
var ErrInvalidMessage = errors.New("i18n: invalid translation message")

// PluralForm names a CLDR cardinal category, not a numeric comparison. The
// numbers belonging to a category depend on the translation's language.
type PluralForm string

const (
	Zero  PluralForm = "zero"
	One   PluralForm = "one"
	Two   PluralForm = "two"
	Few   PluralForm = "few"
	Many  PluralForm = "many"
	Other PluralForm = "other"
)

// Translation stores plain text. It is not executable Go, a template, a printf
// invocation or trusted HTML. Singular entries use Text; plural entries use
// Forms, with Other required. Empty translations are valid intentional output.
type Translation struct {
	ID      string                `json:"id"`
	Context string                `json:"context,omitempty"`
	Text    string                `json:"text,omitempty"`
	Forms   map[PluralForm]string `json:"forms,omitempty"`
}

// Catalog is an application/project-owned source catalog. NewTranslator
// validates and compiles a detached lookup index at startup, never per request.
// Duplicate language/domain/context/ID keys fail instead of depending on order.
type Catalog struct {
	Language string        `json:"language"`
	Domain   string        `json:"domain"`
	Messages []Translation `json:"messages"`
}

type MissingMessage struct {
	Language, Domain, Context, ID string
	Plural                        bool
}

type TranslatorConfig struct {
	Catalogs []Catalog
	// Nil uses the resolver's default language; a nonnil empty list disables
	// additional fallbacks. Each configured fallback must be an allowed tag.
	// Lookup tries the selected tag's CLDR parents before these fallback tags
	// and their parents, then the caller's unchanged source string.
	FallbackLanguages []string
	// Missing runs only when Development is true and all catalog lookups miss.
	// It receives metadata only, never interpolated values or a task payload.
	// The hook must be bounded; errors/panics cannot alter lookup output.
	Development bool
	Missing     func(context.Context, MissingMessage)
}

type messageKey struct{ domain, context, id string }
type compiledTranslation struct {
	text  string
	forms map[PluralForm]string
}
type catalogLanguage struct {
	tag      language.Tag
	messages map[messageKey]compiledTranslation
}

// Translator holds an immutable in-memory catalog. No package-global catalog
// or request language is read or modified, including by lazy message values.
type Translator struct {
	resolver *Resolver
	catalogs map[string]catalogLanguage
	chains   map[string][]string
	missing  func(context.Context, MissingMessage)
}

func validMessagePart(value string, maxBytes int, required bool) bool {
	return (!required || value != "") && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validDomain(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validPluralForm(form PluralForm) bool {
	switch form {
	case Zero, One, Two, Few, Many, Other:
		return true
	}
	return false
}

func parents(tag language.Tag) []language.Tag {
	var result []language.Tag
	for !tag.IsRoot() {
		result = append(result, tag)
		tag = tag.Parent()
	}
	return result
}

func NewTranslator(resolver *Resolver, config TranslatorConfig) (*Translator, error) {
	if resolver == nil || len(resolver.languages) == 0 || len(config.Catalogs) > 4096 || len(config.FallbackLanguages) > 256 {
		return nil, ErrInvalidCatalog
	}
	t := &Translator{resolver: resolver, catalogs: map[string]catalogLanguage{}, chains: map[string][]string{}}
	if config.Development {
		t.missing = config.Missing
	}
	eligible := map[string]bool{}
	for _, tag := range resolver.languages {
		for _, parent := range parents(tag) {
			eligible[parent.String()] = true
		}
	}
	fallbacks := []language.Tag{}
	configured := config.FallbackLanguages
	if configured == nil {
		configured = []string{resolver.Default().Language()}
	}
	seenFallback := map[string]bool{}
	for _, raw := range configured {
		tag, err := parseLanguage(raw)
		if err != nil {
			return nil, ErrInvalidCatalog
		}
		if _, allowed := resolver.allowed[tag.String()]; !allowed || seenFallback[tag.String()] {
			return nil, ErrInvalidCatalog
		}
		seenFallback[tag.String()] = true
		fallbacks = append(fallbacks, tag)
	}
	for _, selected := range resolver.languages {
		seen := map[string]bool{}
		for _, candidate := range append([]language.Tag{selected}, fallbacks...) {
			for _, tag := range parents(candidate) {
				if !seen[tag.String()] {
					t.chains[selected.String()] = append(t.chains[selected.String()], tag.String())
					seen[tag.String()] = true
				}
			}
		}
	}
	entries, bytes := 0, 0
	kinds := map[messageKey]bool{}
	for _, source := range config.Catalogs {
		tag, err := parseLanguage(source.Language)
		if err != nil || !eligible[tag.String()] || !validDomain(source.Domain) {
			return nil, ErrInvalidCatalog
		}
		dictionary, exists := t.catalogs[tag.String()]
		if !exists {
			dictionary = catalogLanguage{tag: tag, messages: map[messageKey]compiledTranslation{}}
		}
		for _, entry := range source.Messages {
			entries++
			if entries > 100000 || !validMessagePart(entry.ID, 4096, true) || !validMessagePart(entry.Context, 512, false) || !validMessagePart(entry.Text, 64<<10, false) || len(entry.Forms) > 6 || entry.Forms != nil && len(entry.Forms) == 0 {
				return nil, ErrInvalidCatalog
			}
			key := messageKey{source.Domain, entry.Context, entry.ID}
			isPlural := len(entry.Forms) > 0
			if prior, found := kinds[key]; found && prior != isPlural {
				return nil, ErrInvalidCatalog
			}
			kinds[key] = isPlural
			if _, duplicate := dictionary.messages[key]; duplicate {
				return nil, ErrInvalidCatalog
			}
			compiled := compiledTranslation{text: entry.Text}
			bytes += len(source.Domain) + len(entry.ID) + len(entry.Context) + len(entry.Text)
			if len(entry.Forms) > 0 {
				if _, other := entry.Forms[Other]; !other || entry.Text != "" {
					return nil, ErrInvalidCatalog
				}
				compiled.forms = map[PluralForm]string{}
				for form, text := range entry.Forms {
					if !validPluralForm(form) || !validMessagePart(text, 64<<10, false) {
						return nil, ErrInvalidCatalog
					}
					bytes += len(text)
					compiled.forms[form] = text
				}
			}
			if bytes > 64<<20 {
				return nil, ErrInvalidCatalog
			}
			dictionary.messages[key] = compiled
		}
		t.catalogs[tag.String()] = dictionary
	}
	return t, nil
}
