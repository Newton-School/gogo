package forms

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrValidation indicates form field validation failed.
var ErrValidation = errors.New("form validation")

// Validator validates one cleaned field value.
type Validator func(any) error

// FieldOptions configures common form field behavior.
type FieldOptions struct {
	Required      bool
	Label         string
	Initial       any
	HelpText      string
	Validators    []Validator
	Disabled      bool
	Localize      bool
	ErrorMessages map[string]string
	Widget        any
}

// Choice is one selectable form value.
type Choice struct {
	Value any
	Label string
}

// UploadedFile is a form-level uploaded file value.
type UploadedFile struct {
	Name        string
	Size        int64
	ContentType string
	Content     []byte
}

// Field parses and validates one form value.
type Field struct {
	Kind       string
	Options    FieldOptions
	Choices    []Choice
	Fields     []*Field
	Regex      *regexp.Regexp
	Coerce     func(string) (any, error)
	CleanFunc  func(any) (any, error)
	Compress   func([]any) (any, error)
	EmptyValue any
}

func BooleanField(options FieldOptions) *Field {
	return &Field{Kind: "boolean", Options: options}
}

func CharField(options FieldOptions) *Field {
	return &Field{Kind: "char", Options: options}
}

func ChoiceField(options FieldOptions, choices []Choice) *Field {
	return &Field{Kind: "choice", Options: options, Choices: cloneChoices(choices)}
}

func TypedChoiceField(options FieldOptions, choices []Choice, coerce func(string) (any, error)) *Field {
	return &Field{Kind: "typed_choice", Options: options, Choices: cloneChoices(choices), Coerce: coerce}
}

func MultipleChoiceField(options FieldOptions, choices []Choice) *Field {
	return &Field{Kind: "multiple_choice", Options: options, Choices: cloneChoices(choices)}
}

func DateField(options FieldOptions) *Field {
	return &Field{Kind: "date", Options: options}
}

func DateTimeField(options FieldOptions) *Field {
	return &Field{Kind: "datetime", Options: options}
}

func TimeField(options FieldOptions) *Field {
	return &Field{Kind: "time", Options: options}
}

func DurationField(options FieldOptions) *Field {
	return &Field{Kind: "duration", Options: options}
}

func DecimalField(options FieldOptions) *Field {
	return &Field{Kind: "decimal", Options: options}
}

func EmailField(options FieldOptions) *Field {
	return &Field{Kind: "email", Options: options}
}

func FileField(options FieldOptions) *Field {
	return &Field{Kind: "file", Options: options}
}

func ImageField(options FieldOptions) *Field {
	return &Field{Kind: "image", Options: options}
}

func FloatField(options FieldOptions) *Field {
	return &Field{Kind: "float", Options: options}
}

func IntegerField(options FieldOptions) *Field {
	return &Field{Kind: "integer", Options: options}
}

func GenericIPAddressField(options FieldOptions) *Field {
	return &Field{Kind: "ip", Options: options}
}

func JSONField(options FieldOptions) *Field {
	return &Field{Kind: "json", Options: options}
}

func ComboField(options FieldOptions, fields ...*Field) *Field {
	return &Field{Kind: "combo", Options: options, Fields: cloneFields(fields)}
}

func MultiValueField(options FieldOptions, fields ...*Field) *Field {
	return &Field{Kind: "multi_value", Options: options, Fields: cloneFields(fields)}
}

func SplitDateTimeField(options FieldOptions) *Field {
	return &Field{Kind: "split_datetime", Options: options}
}

func ModelChoiceField(options FieldOptions, choices []Choice) *Field {
	return &Field{Kind: "model_choice", Options: options, Choices: cloneChoices(choices)}
}

func ModelMultipleChoiceField(options FieldOptions, choices []Choice) *Field {
	return &Field{Kind: "model_multiple_choice", Options: options, Choices: cloneChoices(choices)}
}

func MultipleFileField(options FieldOptions) *Field {
	return &Field{Kind: "multiple_file", Options: options}
}

func RegexField(options FieldOptions, pattern *regexp.Regexp) *Field {
	return &Field{Kind: "regex", Options: options, Regex: pattern}
}

func SlugField(options FieldOptions) *Field {
	return &Field{Kind: "slug", Options: options}
}

func URLField(options FieldOptions) *Field {
	return &Field{Kind: "url", Options: options}
}

func UUIDField(options FieldOptions) *Field {
	return &Field{Kind: "uuid", Options: options}
}

// Clean parses, validates, and returns a cleaned field value.
func (f *Field) Clean(value any) (any, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: field is nil", ErrValidation)
	}
	if f.Options.Disabled {
		return f.Options.Initial, nil
	}
	if emptyValue(value) {
		if f.Options.Required {
			return nil, f.validationError("required", "This field is required.")
		}
		return f.EmptyValue, nil
	}
	cleaned, err := f.cleanValue(value)
	if err != nil {
		return nil, err
	}
	for _, validator := range f.Options.Validators {
		if validator == nil {
			continue
		}
		if err := validator(cleaned); err != nil {
			return nil, f.validationError("invalid", err.Error())
		}
	}
	return cleaned, nil
}

func (f *Field) cleanValue(value any) (any, error) {
	if f.CleanFunc != nil {
		return f.CleanFunc(value)
	}
	switch f.Kind {
	case "boolean":
		return cleanBool(value)
	case "char":
		return fmt.Sprint(value), nil
	case "choice", "model_choice":
		return f.cleanChoice(value)
	case "typed_choice":
		return f.cleanTypedChoice(value)
	case "multiple_choice", "model_multiple_choice":
		return f.cleanMultipleChoice(value)
	case "date":
		return parseDate(value)
	case "datetime":
		return parseDateTime(value)
	case "time":
		return parseTime(value)
	case "duration":
		return time.ParseDuration(fmt.Sprint(value))
	case "decimal":
		return cleanDecimal(value)
	case "email":
		return cleanEmail(value)
	case "file":
		return cleanFile(value)
	case "image":
		return cleanImage(value)
	case "float":
		parsed, err := strconv.ParseFloat(fmt.Sprint(value), 64)
		if err != nil {
			return nil, fmt.Errorf("%w: enter a valid float", ErrValidation)
		}
		return parsed, nil
	case "integer":
		parsed, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: enter a valid integer", ErrValidation)
		}
		return parsed, nil
	case "ip":
		return cleanIP(value)
	case "json":
		return cleanJSON(value)
	case "combo":
		return f.cleanCombo(value)
	case "multi_value":
		return f.cleanMultiValue(value)
	case "split_datetime":
		return cleanSplitDateTime(value)
	case "multiple_file":
		return cleanMultipleFile(value)
	case "regex":
		return f.cleanRegex(value)
	case "slug":
		return cleanSlug(value)
	case "url":
		return cleanURL(value)
	case "uuid":
		return cleanUUID(value)
	default:
		return value, nil
	}
}

func (f *Field) cleanChoice(value any) (any, error) {
	text := fmt.Sprint(value)
	if len(f.Choices) == 0 && f.Kind == "model_choice" {
		return text, nil
	}
	for _, choice := range f.Choices {
		if fmt.Sprint(choice.Value) == text {
			return choice.Value, nil
		}
	}
	return nil, f.validationError("invalid_choice", "Select a valid choice.")
}

func (f *Field) cleanTypedChoice(value any) (any, error) {
	cleaned, err := f.cleanChoice(value)
	if err != nil {
		return nil, err
	}
	if f.Coerce == nil {
		return cleaned, nil
	}
	return f.Coerce(fmt.Sprint(cleaned))
}

func (f *Field) cleanMultipleChoice(value any) ([]string, error) {
	values, err := toStringSlice(value)
	if err != nil {
		return nil, f.validationError("invalid", "Enter a list of values.")
	}
	if len(f.Choices) == 0 && f.Kind == "model_multiple_choice" {
		return values, nil
	}
	for _, value := range values {
		if _, err := f.cleanChoice(value); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (f *Field) cleanCombo(value any) (any, error) {
	cleaned := value
	for _, field := range f.Fields {
		next, err := field.Clean(cleaned)
		if err != nil {
			return nil, err
		}
		cleaned = next
	}
	return cleaned, nil
}

func (f *Field) cleanMultiValue(value any) (any, error) {
	values, err := toAnySlice(value)
	if err != nil || len(values) != len(f.Fields) {
		return nil, f.validationError("invalid", "Enter a complete list of values.")
	}
	cleaned := make([]any, len(values))
	for index, field := range f.Fields {
		cleanedValue, err := field.Clean(values[index])
		if err != nil {
			return nil, err
		}
		cleaned[index] = cleanedValue
	}
	if f.Compress != nil {
		return f.Compress(cleaned)
	}
	return cleaned, nil
}

func (f *Field) cleanRegex(value any) (string, error) {
	text := fmt.Sprint(value)
	if f.Regex == nil || !f.Regex.MatchString(text) {
		return "", f.validationError("invalid", "Enter a valid value.")
	}
	return text, nil
}

func (f *Field) validationError(code, fallback string) error {
	if f.Options.ErrorMessages != nil {
		if message := f.Options.ErrorMessages[code]; message != "" {
			fallback = message
		}
	}
	return fmt.Errorf("%w: %s", ErrValidation, fallback)
}

func cleanBool(value any) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "on", "yes":
			return true, nil
		case "false", "0", "off", "no":
			return false, nil
		}
	}
	return false, fmt.Errorf("%w: enter a valid boolean", ErrValidation)
}

func cleanDecimal(value any) (string, error) {
	text := fmt.Sprint(value)
	if _, err := strconv.ParseFloat(text, 64); err != nil {
		return "", fmt.Errorf("%w: enter a valid decimal", ErrValidation)
	}
	return text, nil
}

func cleanEmail(value any) (string, error) {
	text := fmt.Sprint(value)
	address, err := mail.ParseAddress(text)
	if err != nil || address.Address != text {
		return "", fmt.Errorf("%w: enter a valid email", ErrValidation)
	}
	return text, nil
}

func cleanFile(value any) (any, error) {
	switch typed := value.(type) {
	case UploadedFile:
		if typed.Name == "" {
			return UploadedFile{}, fmt.Errorf("%w: upload a valid file", ErrValidation)
		}
		return typed, nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, fmt.Errorf("%w: upload a valid file", ErrValidation)
		}
		return typed, nil
	default:
		return UploadedFile{}, fmt.Errorf("%w: upload a valid file", ErrValidation)
	}
}

func cleanImage(value any) (any, error) {
	file, err := cleanFile(value)
	if err != nil {
		return UploadedFile{}, err
	}
	name := ""
	switch typed := file.(type) {
	case UploadedFile:
		name = typed.Name
	case string:
		name = typed
	}
	lower := strings.ToLower(name)
	if !(strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".gif")) {
		return UploadedFile{}, fmt.Errorf("%w: upload a valid image", ErrValidation)
	}
	return file, nil
}

func cleanIP(value any) (string, error) {
	text := fmt.Sprint(value)
	if net.ParseIP(text) == nil {
		return "", fmt.Errorf("%w: enter a valid IP address", ErrValidation)
	}
	return text, nil
}

func cleanJSON(value any) (any, error) {
	switch typed := value.(type) {
	case string:
		var decoded any
		if err := json.Unmarshal([]byte(typed), &decoded); err != nil {
			return nil, fmt.Errorf("%w: enter valid JSON", ErrValidation)
		}
		return decoded, nil
	default:
		if _, err := json.Marshal(value); err != nil {
			return nil, fmt.Errorf("%w: enter valid JSON", ErrValidation)
		}
		return value, nil
	}
}

func cleanSplitDateTime(value any) (time.Time, error) {
	values, err := toAnySlice(value)
	if err != nil || len(values) != 2 {
		return time.Time{}, fmt.Errorf("%w: enter date and time", ErrValidation)
	}
	date, err := parseDate(values[0])
	if err != nil {
		return time.Time{}, err
	}
	clock, err := parseTime(values[1])
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(), time.UTC), nil
}

func cleanMultipleFile(value any) ([]UploadedFile, error) {
	switch typed := value.(type) {
	case []UploadedFile:
		return append([]UploadedFile(nil), typed...), nil
	case []any:
		files := make([]UploadedFile, len(typed))
		for i, item := range typed {
			file, ok := item.(UploadedFile)
			if !ok || file.Name == "" {
				return nil, fmt.Errorf("%w: upload valid files", ErrValidation)
			}
			files[i] = file
		}
		return files, nil
	default:
		return nil, fmt.Errorf("%w: upload valid files", ErrValidation)
	}
}

var slugRegexp = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func cleanSlug(value any) (string, error) {
	text := fmt.Sprint(value)
	if !slugRegexp.MatchString(text) {
		return "", fmt.Errorf("%w: enter a valid slug", ErrValidation)
	}
	return text, nil
}

func cleanURL(value any) (string, error) {
	text := fmt.Sprint(value)
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%w: enter a valid URL", ErrValidation)
	}
	return text, nil
}

func cleanUUID(value any) (string, error) {
	text := fmt.Sprint(value)
	if _, err := uuid.Parse(text); err != nil {
		return "", fmt.Errorf("%w: enter a valid UUID", ErrValidation)
	}
	return text, nil
}

func parseDate(value any) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", fmt.Sprint(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: enter a valid date", ErrValidation)
	}
	return parsed, nil
}

func parseDateTime(value any) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, fmt.Sprint(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: enter a valid datetime", ErrValidation)
	}
	return parsed, nil
}

func parseTime(value any) (time.Time, error) {
	parsed, err := time.Parse("15:04:05", fmt.Sprint(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: enter a valid time", ErrValidation)
	}
	return parsed, nil
}

func toStringSlice(value any) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		values := make([]string, len(typed))
		for index, item := range typed {
			values[index] = fmt.Sprint(item)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("%w: not a list", ErrValidation)
	}
}

func toAnySlice(value any) ([]any, error) {
	switch typed := value.(type) {
	case []any:
		return append([]any(nil), typed...), nil
	case []string:
		values := make([]any, len(typed))
		for i, item := range typed {
			values[i] = item
		}
		return values, nil
	default:
		return nil, fmt.Errorf("%w: not a list", ErrValidation)
	}
}

func emptyValue(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return typed == ""
	case []any:
		return len(typed) == 0
	case []string:
		return len(typed) == 0
	case []UploadedFile:
		return len(typed) == 0
	default:
		return false
	}
}

func cloneChoices(choices []Choice) []Choice {
	return append([]Choice(nil), choices...)
}

func cloneFields(fields []*Field) []*Field {
	return append([]*Field(nil), fields...)
}
