package sqlcompiler

import (
	"errors"
	"math"
	"reflect"
	"strings"

	"github.com/Newton-School/gogo/core/db"
)

func IsWindowFunction(name string) bool {
	switch strings.ToUpper(name) {
	case "ROW_NUMBER", "RANK", "DENSE_RANK", "PERCENT_RANK", "CUME_DIST", "NTILE", "LAG", "LEAD", "FIRST_VALUE", "LAST_VALUE", "NTH_VALUE":
		return true
	}
	return false
}

func unsupportedWindow(message string) error {
	return &db.Error{Code: db.UnsupportedFeature, Message: message}
}

func validateWindowShape(expression db.Expression) error {
	if expression.Kind != "window" || len(expression.Args) != 1 || expression.Name != "" || expression.Value != nil || expression.Output != nil || expression.Distinct || expression.Filter != nil || len(expression.Branches) != 0 {
		return errors.New("orm: window requires exactly one function and an OVER specification")
	}
	function := expression.Args[0]
	name := strings.ToUpper(function.Name)
	if function.Kind != "function" || function.Window != nil || function.Value != nil || function.Output != nil || len(function.Branches) != 0 || function.Distinct {
		return unsupportedWindow("Window function metadata or DISTINCT aggregate window is not supported")
	}
	min, max := 0, 0
	switch name {
	case "ROW_NUMBER", "RANK", "DENSE_RANK", "PERCENT_RANK", "CUME_DIST":
	case "NTILE", "FIRST_VALUE", "LAST_VALUE":
		min, max = 1, 1
	case "LAG", "LEAD":
		min, max = 1, 3
	case "NTH_VALUE":
		min, max = 2, 2
	default:
		if !IsAggregateFunction(name) {
			return unsupportedWindow("Function cannot be evaluated over a window")
		}
		min, max = 1, 1
	}
	if len(function.Args) < min || len(function.Args) > max || function.Filter != nil && !IsAggregateFunction(name) {
		return errors.New("orm: invalid window function arguments or filter")
	}
	position := -1
	if name == "NTILE" {
		position = 0
	}
	if name == "NTH_VALUE" || (name == "LAG" || name == "LEAD") && len(function.Args) > 1 {
		position = 1
	}
	if position >= 0 {
		argument := function.Args[position]
		value := reflect.ValueOf(argument.Value)
		valid := argument.Kind == "value" && value.IsValid()
		if valid {
			switch value.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				valid = value.Int() > 0 && value.Int() <= math.MaxInt32
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				valid = value.Uint() > 0 && value.Uint() <= math.MaxInt32
			default:
				valid = false
			}
		}
		if !valid {
			return errors.New("orm: window bucket, position and offset require a positive 32-bit integer literal")
		}
	}
	if expression.Window != nil {
		if len(expression.Window.PartitionBy) > 64 || len(expression.Window.OrderBy) > 64 {
			return errors.New("orm: window partition and order require at most 64 expressions each")
		}
		for _, order := range expression.Window.OrderBy {
			if order.NullsFirst && order.NullsLast {
				return errors.New("orm: window order cannot request both null placements")
			}
		}
	}
	return nil
}

func (c *Compiler) window(expression db.Expression) (result string, err error) {
	before := len(c.Args)
	defer func() {
		if err != nil {
			c.Args = c.Args[:before]
		}
	}()
	if err = validateWindowShape(expression); err != nil {
		return "", err
	}
	features, ok := c.Dialect.(db.FeatureDialect)
	if !ok || !features.SupportsFeature("window") {
		return "", unsupportedWindow("Selected dialect does not support window expressions")
	}
	state := windowTree{compiler: c}
	if _, err = state.expression(expression, true, false, 0); err != nil {
		return "", err
	}
	var frame string
	if expression.Window != nil && expression.Window.Frame != nil {
		if err = validateWindowFrame(features, *expression.Window.Frame); err != nil {
			return "", err
		}
	}
	function := expression.Args[0]
	// Only this validated direct function may compile a bare window function.
	c.windowFunction++
	result, err = c.Expression(function)
	c.windowFunction--
	if err != nil {
		return "", err
	}
	parts := []string{}
	if specification := expression.Window; specification != nil {
		partition := make([]string, len(specification.PartitionBy))
		for i, expression := range specification.PartitionBy {
			partition[i], err = c.Expression(expression)
			if err != nil {
				return "", err
			}
		}
		if len(partition) > 0 {
			parts = append(parts, "PARTITION BY "+strings.Join(partition, ", "))
		}
		orders := make([]string, len(specification.OrderBy))
		for i, order := range specification.OrderBy {
			orders[i], err = c.Expression(order.Expression)
			if err != nil {
				return "", err
			}
			if order.Desc {
				orders[i] += " DESC"
			} else {
				orders[i] += " ASC"
			}
			if order.NullsFirst {
				orders[i] += " NULLS FIRST"
			}
			if order.NullsLast {
				orders[i] += " NULLS LAST"
			}
		}
		if len(orders) > 0 {
			parts = append(parts, "ORDER BY "+strings.Join(orders, ", "))
		}
		if specification.Frame != nil {
			frame = c.windowFrame(*specification.Frame)
			parts = append(parts, frame)
		}
	}
	return result + " OVER (" + strings.Join(parts, " ") + ")", nil
}

func validateWindowFrame(features db.FeatureDialect, frame db.WindowFrame) error {
	feature := ""
	switch frame.Mode {
	case db.WindowRows:
		feature = "window_rows_frame"
	case db.WindowRange:
		feature = "window_range_frame"
	default:
		return errors.New("orm: invalid window frame mode")
	}
	if !features.SupportsFeature(feature) {
		return unsupportedWindow("Selected dialect does not support this window frame mode")
	}
	position := func(bound db.WindowBound) (int, error) {
		switch bound.Kind {
		case db.WindowUnboundedPreceding, db.WindowCurrentRow, db.WindowUnboundedFollowing:
			if bound.Offset != 0 {
				return 0, errors.New("orm: non-offset window bound contains an offset")
			}
			if bound.Kind == db.WindowUnboundedPreceding {
				return 0, nil
			}
			if bound.Kind == db.WindowCurrentRow {
				return 2, nil
			}
			return 4, nil
		case db.WindowPreceding, db.WindowFollowing:
			if bound.Offset < 0 {
				return 0, errors.New("orm: window offset must be non-negative")
			}
			if frame.Mode == db.WindowRange {
				return 0, unsupportedWindow("RANGE offsets require a typed numeric or interval offset contract")
			}
			if bound.Kind == db.WindowPreceding {
				return 1, nil
			}
			return 3, nil
		default:
			return 0, errors.New("orm: invalid window frame bound")
		}
	}
	start, err := position(frame.Start)
	if err != nil {
		return err
	}
	end, err := position(frame.End)
	if err != nil {
		return err
	}
	if start == 4 || end == 0 || start > end {
		return errors.New("orm: invalid window frame boundary order")
	}
	switch frame.Exclusion {
	case "":
	case db.WindowExcludeCurrentRow, db.WindowExcludeGroup, db.WindowExcludeTies, db.WindowExcludeNoOthers:
		if !features.SupportsFeature("window_frame_exclusion") {
			return unsupportedWindow("Selected dialect does not support window frame exclusions")
		}
	default:
		return errors.New("orm: invalid window frame exclusion")
	}
	return nil
}

func (c *Compiler) windowFrame(frame db.WindowFrame) string {
	bound := func(value db.WindowBound) string {
		switch value.Kind {
		case db.WindowUnboundedPreceding:
			return "UNBOUNDED PRECEDING"
		case db.WindowCurrentRow:
			return "CURRENT ROW"
		case db.WindowUnboundedFollowing:
			return "UNBOUNDED FOLLOWING"
		case db.WindowPreceding:
			return c.bound(value.Offset) + " PRECEDING"
		default:
			return c.bound(value.Offset) + " FOLLOWING"
		}
	}
	result := strings.ToUpper(string(frame.Mode)) + " BETWEEN " + bound(frame.Start) + " AND " + bound(frame.End)
	if frame.Exclusion != "" {
		result += " EXCLUDE " + strings.ToUpper(strings.ReplaceAll(string(frame.Exclusion), "_", " "))
	}
	return result
}
