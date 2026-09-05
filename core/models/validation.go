package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/mail"
	"net/netip"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const NonFieldErrors = "__all__"

type FieldError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Message }

type ValidationError struct {
	Fields map[string][]FieldError `json:"fields"`
}

func (e *ValidationError) Error() string { return "model validation failed" }
func (e *ValidationError) Add(field, code, message string) {
	if field == "" {
		field = NonFieldErrors
	}
	if e.Fields == nil {
		e.Fields = map[string][]FieldError{}
	}
	e.Fields[field] = append(e.Fields[field], FieldError{Code: code, Message: message})
}
func (e *ValidationError) Empty() bool { return e == nil || len(e.Fields) == 0 }
func (e *ValidationError) Merge(field string, err error) {
	if err == nil {
		return
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		for key, values := range ve.Fields {
			if e.Fields == nil {
				e.Fields = map[string][]FieldError{}
			}
			e.Fields[key] = append(e.Fields[key], values...)
		}
		return
	}
	var fe FieldError
	if errors.As(err, &fe) {
		e.Add(field, fe.Code, fe.Message)
		return
	}
	e.Add(field, "invalid", err.Error())
}
func Invalid(code, message string) error { return FieldError{Code: code, Message: message} }

type CleanOptions struct {
	Exclude                     []string
	SkipUnique, SkipConstraints bool
}
type ConstraintChecker interface {
	ValidateUnique(context.Context, Record, []string) error
	ValidateConstraints(context.Context, Record, []string) error
}

// RelationChecker participates in clean_fields, independently of the optional
// unique/constraint stages. Backends implement existence checks; application
// authorization and related-choice scoping remain distinct responsibilities.
type RelationChecker interface {
	ValidateRelation(context.Context, Record, Field) error
}
type Cleaner interface{ Clean(context.Context) error }

func FullClean(ctx context.Context, record Record, options CleanOptions, checker ConstraintChecker) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	exclude := map[string]bool{}
	for _, name := range options.Exclude {
		exclude[name] = true
	}
	validation := &ValidationError{}
	for _, f := range record.Schema().Fields {
		if exclude[f.Name] || !f.IsStored() || f.Kind == Generated {
			continue
		}
		value, err := record.Get(f.Name)
		if err == nil {
			value, err = f.Clean(ctx, value)
		}
		if err == nil {
			err = record.Set(f.Name, value)
		}
		if err == nil && (f.Kind == ForeignKey || f.Kind == OneToOne) && value != nil {
			if relations, ok := checker.(RelationChecker); ok {
				err = relations.ValidateRelation(ctx, record, f)
			} else {
				err = errors.New("models: database relation checker is required")
			}
		}
		validation.Merge(f.Name, err)
	}
	if bound, ok := record.(*BoundRecord); ok {
		if cleaner, ok := bound.model.(Cleaner); ok {
			validation.Merge(NonFieldErrors, cleaner.Clean(ctx))
		}
	}
	currentExclusions := func() []string {
		names := append([]string(nil), options.Exclude...)
		for name := range validation.Fields {
			if name != NonFieldErrors {
				names = append(names, name)
			}
		}
		return names
	}
	if checker != nil {
		for _, stage := range []struct {
			skip  bool
			check func(context.Context, Record, []string) error
		}{{options.SkipUnique, checker.ValidateUnique}, {options.SkipConstraints, checker.ValidateConstraints}} {
			if stage.skip {
				continue
			}
			err := stage.check(ctx, record, currentExclusions())
			if err != nil {
				var ve *ValidationError
				var fe FieldError
				if !errors.As(err, &ve) && !errors.As(err, &fe) {
					return errors.Join(validationIfAny(validation), err)
				}
				validation.Merge(NonFieldErrors, err)
			}
		}
	} else {
		needsUnique := false
		for _, f := range record.Schema().Fields {
			needsUnique = needsUnique || f.Unique || f.UniqueForDate != "" || f.UniqueForMonth != "" || f.UniqueForYear != ""
		}
		if (!options.SkipUnique && needsUnique) || (!options.SkipConstraints && len(record.Schema().Constraints) > 0) {
			return errors.Join(validationIfAny(validation), errors.New("models: database constraint checker is required"))
		}
	}
	return validationIfAny(validation)
}
func validationIfAny(e *ValidationError) error {
	if e.Empty() {
		return nil
	}
	return e
}
func (f Field) Validate(ctx context.Context, value any) error {
	_, err := f.Clean(ctx, value)
	return err
}

var slugPattern = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (f Field) Clean(ctx context.Context, value any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if value == nil {
		// An automatic identity may be absent until INSERT allocates it. This
		// validation exception does not make its database column nullable or
		// permit missing user-assigned primary keys.
		if f.Null || f.IsAuto() {
			return nil, nil
		}
		return nil, Invalid("null", "This field cannot be null.")
	}
	if text, ok := value.(string); ok && text == "" {
		if f.Blank {
			return value, nil
		}
		return nil, Invalid("blank", "This field cannot be blank.")
	}
	var err error
	switch f.Kind {
	case SmallInteger, Integer, BigInteger, PositiveSmallInteger, PositiveInteger, PositiveBigInteger, SmallAuto, Auto, BigAuto:
		var integer int64
		integer, err = integerValue(value)
		if err != nil {
			return nil, Invalid("invalid", "Enter a whole number.")
		}
		low, high := int64(math.MinInt64), int64(math.MaxInt64)
		switch f.Kind {
		case SmallInteger, SmallAuto:
			low, high = math.MinInt16, math.MaxInt16
		case Integer, Auto:
			low, high = math.MinInt32, math.MaxInt32
		case PositiveSmallInteger:
			low, high = 0, math.MaxInt16
		case PositiveInteger:
			low, high = 0, math.MaxInt32
		case PositiveBigInteger:
			low = 0
		}
		if integer < low || integer > high {
			return nil, Invalid("range", "Number is outside the field range.")
		}
		value = integer
	case Decimal:
		text := fmt.Sprint(value)
		if _, ok := new(big.Rat).SetString(text); !ok || strings.ContainsAny(text, "/eE") {
			return nil, Invalid("invalid", "Enter a decimal number without an exponent.")
		}
		text = strings.TrimPrefix(strings.TrimPrefix(text, "-"), "+")
		parts := strings.Split(text, ".")
		whole := len(strings.TrimLeft(parts[0], "0"))
		places := 0
		if len(parts) > 1 {
			places = len(parts[1])
		}
		if places > f.DecimalPlaces || whole > f.MaxDigits-f.DecimalPlaces || whole+places > f.MaxDigits {
			return nil, Invalid("precision", "Decimal exceeds declared precision.")
		}
		value = fmt.Sprint(value)
	case Float:
		v, parseErr := strconv.ParseFloat(fmt.Sprint(value), 64)
		if parseErr != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return nil, Invalid("invalid", "Enter a finite number.")
		}
		value = v
	case Boolean:
		if _, ok := value.(bool); !ok {
			v, parseErr := strconv.ParseBool(fmt.Sprint(value))
			if parseErr != nil {
				return nil, Invalid("invalid", "Enter a boolean value.")
			}
			value = v
		}
	case Char, Text, Slug, Email, URL, GenericIPAddress, UUID, FilePath, File, Image:
		text, ok := value.(string)
		if !ok {
			return nil, Invalid("invalid", "Enter a string value.")
		}
		length := utf8.RuneCountInString(text)
		if !utf8.ValidString(text) {
			return nil, Invalid("invalid", "Enter valid UTF-8 text.")
		}
		if f.MaxLength > 0 && length > f.MaxLength {
			return nil, Invalid("max_length", "Value is too long.")
		}
		if length < f.MinLength {
			return nil, Invalid("min_length", "Value is too short.")
		}
		switch f.Kind {
		case Slug:
			if !slugPattern.MatchString(text) {
				return nil, Invalid("invalid", "Enter letters, numbers, underscores or hyphens.")
			}
		case Email:
			address, e := mail.ParseAddress(text)
			if e != nil || address.Address != text || strings.ContainsAny(text, "\r\n") {
				return nil, Invalid("invalid", "Enter a valid email address.")
			}
		case URL:
			u, e := url.Parse(text)
			if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ftp" && u.Scheme != "ftps") {
				return nil, Invalid("invalid", "Enter a valid absolute URL.")
			}
		case GenericIPAddress:
			if _, e := netip.ParseAddr(text); e != nil {
				return nil, Invalid("invalid", "Enter a valid IP address.")
			}
		case UUID:
			if !uuidPattern.MatchString(text) {
				return nil, Invalid("invalid", "Enter a valid UUID.")
			}
		}
	case Date, DateTime, Time:
		if _, ok := value.(time.Time); !ok {
			format := time.RFC3339Nano
			if f.Kind == Date {
				format = "2006-01-02"
			}
			if f.Kind == Time {
				format = "15:04:05.999999"
			}
			value, err = time.Parse(format, fmt.Sprint(value))
			if err != nil {
				return nil, Invalid("invalid", "Enter a valid date or time.")
			}
		}
		if f.Kind == DateTime {
			value = value.(time.Time).UTC()
		}
	case Duration:
		if _, ok := value.(time.Duration); !ok {
			value, err = time.ParseDuration(fmt.Sprint(value))
			if err != nil {
				return nil, Invalid("invalid", "Enter a valid duration.")
			}
		}
	case Binary:
		if _, ok := value.([]byte); !ok {
			return nil, Invalid("invalid", "Enter binary bytes.")
		}
	case JSON:
		var encoded []byte
		switch v := value.(type) {
		case []byte:
			encoded = v
		case json.RawMessage:
			encoded = v
		default:
			// Model values are native JSON values, not form-encoded text.
			// A Go string remains a JSON string even when it spells "null",
			// a number, or an object. RawMessage explicitly opts into parsing.
			encoded, err = json.Marshal(value)
		}
		if err != nil || len(encoded) > 1024*1024 || !json.Valid(encoded) {
			return nil, Invalid("invalid", "Enter valid JSON within the size limit.")
		}
		var normalized any
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.UseNumber()
		if decoder.Decode(&normalized) != nil || jsonDepth(normalized) > 64 {
			return nil, Invalid("invalid", "JSON nesting exceeds the limit.")
		}
		if normalized == nil {
			normalized = JSONNull
		}
		value = normalized
	case Array:
		v := reflect.ValueOf(value)
		if v.Kind() != reflect.Array && v.Kind() != reflect.Slice {
			return nil, Invalid("invalid", "Enter an array.")
		}
		if f.Element == nil {
			return nil, Invalid("configuration", "Array element descriptor is missing.")
		}
		for i := 0; i < v.Len(); i++ {
			if err := f.Element.Validate(ctx, v.Index(i).Interface()); err != nil {
				return nil, err
			}
		}
	}
	if len(f.Choices) > 0 {
		found := false
		for _, choice := range f.Choices {
			if reflect.DeepEqual(choice.Value, value) || fmt.Sprint(choice.Value) == fmt.Sprint(value) {
				found = true
				break
			}
		}
		if !found {
			return nil, Invalid("invalid_choice", "Select a valid choice.")
		}
	}
	if f.Min != nil || f.Max != nil {
		number, ok := new(big.Rat).SetString(fmt.Sprint(value))
		if !ok {
			return nil, Invalid("invalid", "Value is not numeric.")
		}
		if f.Min != nil {
			min, ok := new(big.Rat).SetString(fmt.Sprint(f.Min))
			if !ok {
				return nil, Invalid("configuration", "Invalid minimum.")
			}
			if number.Cmp(min) < 0 {
				return nil, Invalid("min_value", "Value is below the minimum.")
			}
		}
		if f.Max != nil {
			max, ok := new(big.Rat).SetString(fmt.Sprint(f.Max))
			if !ok {
				return nil, Invalid("configuration", "Invalid maximum.")
			}
			if number.Cmp(max) > 0 {
				return nil, Invalid("max_value", "Value exceeds the maximum.")
			}
		}
	}
	for _, validator := range f.Validators {
		if err := validator(ctx, value); err != nil {
			return nil, err
		}
	}
	return value, nil
}
func integerValue(value any) (int64, error) {
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.Uint() > math.MaxInt64 {
			return 0, errors.New("overflow")
		}
		return int64(v.Uint()), nil
	}
	return strconv.ParseInt(fmt.Sprint(value), 10, 64)
}
func jsonDepth(value any) int {
	max := 0
	switch v := value.(type) {
	case map[string]any:
		for _, x := range v {
			if n := jsonDepth(x); n > max {
				max = n
			}
		}
		return max + 1
	case []any:
		for _, x := range v {
			if n := jsonDepth(x); n > max {
				max = n
			}
		}
		return max + 1
	}
	return 0
}
