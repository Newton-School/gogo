// Package i18n owns request/task-local language and timezone selection.
// It never changes the process locale, time.Local or stored UTC instants.
package i18n

import (
	"context"
	"errors"
	"net/http"
	"time"
	_ "time/tzdata" // Keep explicitly configured IANA zones portable in Go binaries.

	"golang.org/x/text/language"
)

var ErrInvalidLocale = errors.New("i18n: invalid or unsupported locale configuration")
var ErrUnavailable = errors.New("i18n: locale preference unavailable")

// Preferences is plain data suitable for application-owned profile/task input.
// A timezone is independent of language; it is never inferred from a language
// tag or an untrusted arbitrary request header. Empty fields mean no preference.
type Preferences struct {
	Language string `json:"language,omitempty"`
	TimeZone string `json:"time_zone,omitempty"`
}

// Locale is an immutable selection. Its zero value is not a resolved locale.
type Locale struct {
	tag  language.Tag
	zone *time.Location
}

func (l Locale) Language() string  { return l.tag.String() }
func (l Locale) Tag() language.Tag { return l.tag }
func (l Locale) Location() *time.Location {
	zone := l.zone
	if zone == nil {
		zone = time.UTC
	}
	// Location has no public mutable fields, but callers can still assign an
	// entire value through its pointer. Never expose the resolver's pointer.
	// String initializes a lazy local location before copying (zero/default
	// locales use UTC, and configured host-dependent Local is rejected).
	_ = zone.String()
	copy := *zone
	return &copy
}
func (l Locale) TimeZone() string {
	if l.zone == nil {
		return "UTC"
	}
	return l.zone.String()
}
func (l Locale) Preferences() Preferences {
	return Preferences{Language: l.Language(), TimeZone: l.TimeZone()}
}
func (l Locale) LocalTime(instant time.Time) time.Time { return instant.In(l.Location()) }

type localeKey struct{}

func FromContext(ctx context.Context) (Locale, bool) {
	if ctx == nil {
		return Locale{}, false
	}
	locale, ok := ctx.Value(localeKey{}).(Locale)
	return locale, ok && locale.zone != nil
}

type Config struct {
	// Languages is a bounded nonempty allowlist of BCP 47 tags. The first is
	// the default unless DefaultLanguage explicitly selects another entry.
	Languages       []string
	DefaultLanguage string
	// TimeZones is an explicit IANA allowlist, loaded once at construction.
	// Empty means the default zone alone; DefaultTimeZone defaults to UTC.
	// Host-dependent "Local" is never accepted.
	TimeZones       []string
	DefaultTimeZone string
	LanguageCookie  string
	DisableCookie   bool
	// UserPreferences runs after authentication middleware, if supplied. It
	// reads trusted current profile data; identity is never inferred here.
	// Errors/cancellation/panics fail closed, not silently select another user.
	// Request clones isolate its header/URL changes; it must not consume Body.
	UserPreferences func(*http.Request) (Preferences, error)
}

// Resolver is immutable after construction and safe to share across requests.
type Resolver struct {
	languages     []language.Tag
	allowed       map[string]language.Tag
	zones         map[string]*time.Location
	matcher       language.Matcher
	defaultLocale Locale
	cookie        string
	preferences   func(*http.Request) (Preferences, error)
}

func parseLanguage(value string) (language.Tag, error) {
	if len(value) < 1 || len(value) > 128 {
		return language.Und, ErrInvalidLocale
	}
	tag, err := language.Parse(value)
	if err != nil || tag.IsRoot() {
		return language.Und, ErrInvalidLocale
	}
	return tag, nil
}

func New(config Config) (*Resolver, error) {
	if len(config.Languages) < 1 || len(config.Languages) > 256 || len(config.TimeZones) > 256 {
		return nil, ErrInvalidLocale
	}
	r := &Resolver{allowed: map[string]language.Tag{}, zones: map[string]*time.Location{}, preferences: config.UserPreferences}
	for _, value := range config.Languages {
		tag, err := parseLanguage(value)
		if err != nil {
			return nil, err
		}
		if _, duplicate := r.allowed[tag.String()]; duplicate {
			return nil, ErrInvalidLocale
		}
		r.allowed[tag.String()] = tag
		r.languages = append(r.languages, tag)
	}
	selected := r.languages[0]
	if config.DefaultLanguage != "" {
		tag, err := parseLanguage(config.DefaultLanguage)
		if err != nil {
			return nil, err
		}
		var ok bool
		selected, ok = r.allowed[tag.String()]
		if !ok {
			return nil, ErrInvalidLocale
		}
	}
	// Matcher fallback is the first supported tag. Return the matched allowlist
	// entry, not extensions copied from client tags by the matcher.
	ordered := []language.Tag{selected}
	for _, tag := range r.languages {
		if tag != selected {
			ordered = append(ordered, tag)
		}
	}
	r.languages = ordered
	r.matcher = language.NewMatcher(ordered)
	if config.DefaultTimeZone == "" {
		config.DefaultTimeZone = "UTC"
	}
	zones := config.TimeZones
	if len(zones) == 0 {
		zones = []string{config.DefaultTimeZone}
	}
	for _, name := range zones {
		if name == "" || name == "Local" || len(name) > 128 {
			return nil, ErrInvalidLocale
		}
		if _, duplicate := r.zones[name]; duplicate {
			return nil, ErrInvalidLocale
		}
		zone, err := time.LoadLocation(name)
		if err != nil {
			return nil, ErrInvalidLocale
		}
		r.zones[name] = zone
	}
	zone, ok := r.zones[config.DefaultTimeZone]
	if !ok {
		return nil, ErrInvalidLocale
	}
	r.defaultLocale = Locale{tag: selected, zone: zone}
	if !config.DisableCookie {
		r.cookie = config.LanguageCookie
		if r.cookie == "" {
			r.cookie = "gogo_language"
		}
		if len(r.cookie) > 128 || (&http.Cookie{Name: r.cookie, Value: "valid"}).Valid() != nil {
			return nil, ErrInvalidLocale
		}
	}
	return r, nil
}

func (r *Resolver) Default() Locale {
	if r == nil {
		return Locale{}
	}
	return r.defaultLocale
}

// WithLocale validates an explicit request/task override against this resolver.
// Empty preferences retain the inherited selection or this resolver's default.
// Explicit invalid values return an error rather than silently changing intent.
func (r *Resolver) WithLocale(ctx context.Context, preferences Preferences) (context.Context, error) {
	if ctx == nil || r == nil {
		return nil, ErrInvalidLocale
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current := r.defaultLocale
	if inherited, found := FromContext(ctx); found {
		current = inherited
	}
	if preferences.Language != "" {
		tag, err := parseLanguage(preferences.Language)
		if err != nil {
			return nil, err
		}
		current.tag = tag
	}
	if preferences.TimeZone != "" {
		zone, found := r.zones[preferences.TimeZone]
		if !found {
			return nil, ErrInvalidLocale
		}
		current.zone = zone
	}
	if _, found := r.allowed[current.Language()]; !found {
		return nil, ErrInvalidLocale
	}
	zone, found := r.zones[current.TimeZone()]
	if !found {
		return nil, ErrInvalidLocale
	}
	current.zone = zone
	return context.WithValue(ctx, localeKey{}, current), nil
}
