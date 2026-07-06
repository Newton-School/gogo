package admin

import (
	"errors"
	"fmt"

	"github.com/Newton-School/gogo/models"
)

var ErrInvalidModelAdminOption = errors.New("invalid model admin option")

// Validate checks ModelAdmin option combinations for one model.
func (a ModelAdmin) Validate(meta models.Metadata) error {
	listDisplay := setFromSlice(a.ListDisplay)
	listDisplayLinks := setFromSlice(a.ListDisplayLinks)
	for _, field := range a.ListEditable {
		if _, ok := listDisplay[field]; !ok {
			return fmt.Errorf("%w: list_editable field %s must be in list_display for %s", ErrInvalidModelAdminOption, field, meta.Label())
		}
		if _, ok := listDisplayLinks[field]; ok {
			return fmt.Errorf("%w: list_editable field %s cannot be in list_display_links for %s", ErrInvalidModelAdminOption, field, meta.Label())
		}
	}
	return nil
}

func setFromSlice(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func cloneStringSliceMap(values map[string][]string) map[string][]string {
	if values == nil {
		return nil
	}
	copied := make(map[string][]string, len(values))
	for key, value := range values {
		copied[key] = append([]string(nil), value...)
	}
	return copied
}
