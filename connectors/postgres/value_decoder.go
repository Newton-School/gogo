package postgres

import (
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

var _ db.FieldValueDecoder = Dialect{}

// DecodeFieldValue turns PostgreSQL's fixed interval representation into a Go
// duration only when the model declares Duration. Calendar months/years cannot
// be interpreted without an anchor date and are rejected, as are values outside
// time.Duration's range. Raw SQL interval values remain unchanged.
func (Dialect) DecodeFieldValue(field models.Field, value any) (any, error) {
	if value == nil || field.Kind != models.Duration {
		return value, nil
	}
	if duration, ok := value.(time.Duration); ok {
		return duration, nil
	}
	var raw string
	switch value := value.(type) {
	case string:
		raw = value
	case []byte:
		raw = string(value)
	default:
		return nil, errors.New("postgres: invalid Duration database value")
	}
	return fixedInterval(raw)
}

// database/sql exposes pgx intervals in PostgreSQL text form. Keep parsing in
// this connector, use integer microseconds throughout, and never wrap a large
// interval into a plausible but incorrect Go duration.
func fixedInterval(raw string) (time.Duration, error) {
	invalid := errors.New("postgres: Duration requires a finite fixed interval within Go duration range")
	if len(raw) == 0 || len(raw) > 256 {
		return 0, invalid
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 || len(parts) > 9 {
		return 0, invalid
	}
	microseconds := new(big.Int)
	for len(parts) >= 2 {
		quantity, ok := new(big.Int).SetString(parts[0], 10)
		if !ok {
			return 0, invalid
		}
		switch parts[1] {
		case "year", "years", "mon", "mons", "month", "months":
			if quantity.Sign() != 0 {
				return 0, invalid
			}
		case "day", "days":
			microseconds.Add(microseconds, quantity.Mul(quantity, big.NewInt(86_400_000_000)))
		default:
			return 0, invalid
		}
		parts = parts[2:]
	}
	if len(parts) == 1 {
		clock := parts[0]
		negative := strings.HasPrefix(clock, "-")
		if strings.HasPrefix(clock, "-") || strings.HasPrefix(clock, "+") {
			clock = clock[1:]
		}
		components := strings.Split(clock, ":")
		if len(components) != 3 {
			return 0, invalid
		}
		total := new(big.Int)
		for i, component := range components {
			fraction := ""
			if i == 2 {
				var found bool
				component, fraction, found = strings.Cut(component, ".")
				if found && (fraction == "" || len(fraction) > 6) {
					return 0, invalid
				}
			}
			if !intervalDigits(component) || fraction != "" && !intervalDigits(fraction) {
				return 0, invalid
			}
			quantity, ok := new(big.Int).SetString(component, 10)
			if !ok || i > 0 && quantity.Cmp(big.NewInt(59)) > 0 {
				return 0, invalid
			}
			scale := []int64{3_600_000_000, 60_000_000, 1_000_000}[i]
			total.Add(total, quantity.Mul(quantity, big.NewInt(scale)))
			if fraction != "" {
				fraction += strings.Repeat("0", 6-len(fraction))
				micros, _ := new(big.Int).SetString(fraction, 10)
				total.Add(total, micros)
			}
		}
		if negative {
			total.Neg(total)
		}
		microseconds.Add(microseconds, total)
	}
	nanoseconds := microseconds.Mul(microseconds, big.NewInt(1000))
	if !nanoseconds.IsInt64() {
		return 0, invalid
	}
	return time.Duration(nanoseconds.Int64()), nil
}

func intervalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
