package postgres

import (
	"errors"
	"reflect"
)

const catalogTextLimit = 64 << 10
const catalogByteLimit = 8 << 20

// The wire-side left(...,65537) bounds individual catalog definitions before
// scanning. This byte budget additionally bounds aggregate metadata retained
// across every selected relation; oversized input is never silently truncated.
type catalogBudget int

func (b *catalogBudget) add(limit int, values ...string) error {
	for _, value := range values {
		if len(value) > limit || int(*b) > catalogByteLimit-len(value) {
			return errors.New("postgres: inspection metadata exceeds the 64 KiB text or 8 MiB catalog limit; select fewer relations or review oversized definitions separately")
		}
		*b += catalogBudget(len(value))
	}
	return nil
}

func catalogNil(value any) bool {
	if value == nil {
		return true
	}
	r := reflect.ValueOf(value)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	default:
		return false
	}
}
