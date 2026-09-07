package i18n

import (
	"context"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

func cardinalForm(tag language.Tag, count int64) PluralForm {
	// CLDR cardinal categories use absolute magnitude. Avoid signed overflow
	// for MinInt64 and avoid floating point entirely. MatchPlural supports
	// operands modulo 10,000,000; retain an added modulus for large values so
	// they cannot accidentally equal a small literal such as English 1.
	magnitude := uint64(count)
	if count < 0 {
		magnitude = uint64(-(count + 1)) + 1
	}
	if magnitude >= 10000000 {
		magnitude = 10000000 + magnitude%10000000
	}
	switch plural.Cardinal.MatchPlural(tag, int(magnitude), 0, 0, 0, 0) {
	case plural.Zero:
		return Zero
	case plural.One:
		return One
	case plural.Two:
		return Two
	case plural.Few:
		return Few
	case plural.Many:
		return Many
	default:
		return Other
	}
}

func (t *Translator) lookup(ctx context.Context, domain, messageContext, singular, pluralText string, count int64, isPlural bool) (string, error) {
	if ctx == nil || t == nil || t.resolver == nil || !validDomain(domain) || !validMessagePart(messageContext, 512, false) || !validMessagePart(singular, 4096, true) || isPlural && !validMessagePart(pluralText, 64<<10, true) {
		return "", ErrInvalidMessage
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	selected := t.resolver.Default()
	if inherited, found := FromContext(ctx); found {
		if _, err := t.resolver.WithLocale(ctx, Preferences{}); err != nil {
			return "", err
		}
		selected = inherited
	}
	key := messageKey{domain, messageContext, singular}
	for _, name := range t.chains[selected.Language()] {
		dictionary, exists := t.catalogs[name]
		if !exists {
			continue
		}
		entry, found := dictionary.messages[key]
		if !found {
			continue
		}
		if isPlural != (len(entry.forms) > 0) {
			return "", ErrInvalidMessage
		}
		if !isPlural {
			return translationResult(ctx, entry.text)
		}
		form := cardinalForm(dictionary.tag, count)
		text, found := entry.forms[form]
		if !found {
			text = entry.forms[Other]
		}
		return translationResult(ctx, text)
	}
	if t.missing != nil {
		func() {
			defer func() { _ = recover() }()
			t.missing(ctx, MissingMessage{Language: selected.Language(), Domain: domain, Context: messageContext, ID: singular, Plural: isPlural})
		}()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if isPlural && count != 1 {
		return pluralText, nil
	}
	return singular, nil
}

func translationResult(ctx context.Context, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return text, nil
}

// Gettext looks up a plain source message in an explicit app/project domain.
// The unchanged source string is the final fallback. No interpolation occurs.
func (t *Translator) Gettext(ctx context.Context, domain, message string) (string, error) {
	return t.lookup(ctx, domain, "", message, "", 0, false)
}

func (t *Translator) Pgettext(ctx context.Context, domain, messageContext, message string) (string, error) {
	return t.lookup(ctx, domain, messageContext, message, "", 0, false)
}

// Ngettext selects a CLDR category in the language of the catalog that actually
// supplies the translation. A missing category uses that entry's Other form.
// Source fallback uses singular for exactly 1 and plural for every other count.
func (t *Translator) Ngettext(ctx context.Context, domain, singular, pluralText string, count int64) (string, error) {
	return t.lookup(ctx, domain, "", singular, pluralText, count, true)
}

func (t *Translator) NPgettext(ctx context.Context, domain, messageContext, singular, pluralText string, count int64) (string, error) {
	return t.lookup(ctx, domain, messageContext, singular, pluralText, count, true)
}

// LazyMessage retains source data, not a locale or context. It has no String
// method: resolving without an explicit context would capture a global/default
// language too early. Resolve the same value independently in each request/task.
type LazyMessage struct {
	Domain, Context, Singular, Plural string
}

func (message LazyMessage) Resolve(ctx context.Context, translator *Translator) (string, error) {
	if message.Plural != "" {
		return "", ErrInvalidMessage
	}
	return translator.Pgettext(ctx, message.Domain, message.Context, message.Singular)
}

func (message LazyMessage) ResolveCount(ctx context.Context, translator *Translator, count int64) (string, error) {
	if message.Plural == "" {
		return "", ErrInvalidMessage
	}
	return translator.NPgettext(ctx, message.Domain, message.Context, message.Singular, message.Plural, count)
}
