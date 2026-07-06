package admin

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/models"
)

var ErrInvalidInlineFormset = errors.New("invalid inline formset")

// InlineInput stores request-specific inline formset inputs.
type InlineInput struct {
	ParentID string
	User     auth.User
	Request  *http.Request
	Rows     map[string][]map[string]any
}

// InlineFormset stores render-ready inline form metadata.
type InlineFormset struct {
	Model          string
	Meta           models.Metadata
	Prefix         string
	Kind           InlineKind
	Forms          []InlineForm
	ExtraForms     int
	InitialForms   int
	MinNum         int
	MaxNum         int
	CanDelete      bool
	CanAdd         bool
	CanChange      bool
	CanView        bool
	ShowChangeLink bool
	FKName         string
	ParentID       string
	Fields         []string
	Errors         []string
	Submitted      bool
}

// InlineForm stores one inline form row.
type InlineForm struct {
	Index  int
	Values map[string]any
	Delete bool
	Errors map[string]string
}

// InlineStore persists inline save/delete operations.
type InlineStore interface {
	SaveInline(map[string]any) error
	DeleteInline(map[string]any) error
}

// BuildInlineFormsets builds permission-filtered inline formsets.
func BuildInlineFormsets(inlines []Inline, input InlineInput) []InlineFormset {
	request := input.Request
	if request == nil {
		request, _ = http.NewRequest(http.MethodGet, "/", nil)
	}
	formsets := make([]InlineFormset, 0, len(inlines))
	for _, inline := range inlines {
		if inline.HasPermission != nil && !inline.HasPermission(request, input.User) {
			continue
		}
		kind := inline.Kind
		if kind == "" {
			kind = InlineStacked
		}
		rows := input.Rows[inline.Model]
		forms := make([]InlineForm, len(rows))
		for i, row := range rows {
			forms[i] = InlineForm{Values: cloneRow(row)}
		}
		formsets = append(formsets, InlineFormset{
			Model:          inline.Model,
			Kind:           kind,
			Forms:          forms,
			ExtraForms:     inline.Extra,
			MinNum:         inline.MinNum,
			MaxNum:         inline.MaxNum,
			CanDelete:      inline.CanDelete,
			ShowChangeLink: inline.ShowChangeLink,
			FKName:         inline.FKName,
			ParentID:       input.ParentID,
		})
	}
	return formsets
}

// ValidateInlineFormset validates min and max form constraints.
func ValidateInlineFormset(formset InlineFormset) error {
	count := len(formset.Forms)
	if count < formset.MinNum {
		return fmt.Errorf("%w: %s has too few forms", ErrInvalidInlineFormset, formset.Model)
	}
	if formset.MaxNum > 0 && count > formset.MaxNum {
		return fmt.Errorf("%w: %s has too many forms", ErrInvalidInlineFormset, formset.Model)
	}
	return nil
}

// SaveInlineFormset saves changed forms and deletes marked forms.
func SaveInlineFormset(formset InlineFormset, store InlineStore) error {
	if err := ValidateInlineFormset(formset); err != nil {
		return err
	}
	if store == nil {
		return nil
	}
	for _, form := range formset.Forms {
		if form.Delete {
			if !formset.CanDelete {
				return fmt.Errorf("%w: %s cannot delete", ErrInvalidInlineFormset, formset.Model)
			}
			if err := store.DeleteInline(cloneRow(form.Values)); err != nil {
				return err
			}
			continue
		}
		if err := store.SaveInline(cloneRow(form.Values)); err != nil {
			return err
		}
	}
	return nil
}

// MemoryInlineStore records inline operations in memory.
type MemoryInlineStore struct {
	Saved   []map[string]any
	Deleted []map[string]any
}

// NewMemoryInlineStore creates an empty inline operation recorder.
func NewMemoryInlineStore() *MemoryInlineStore {
	return &MemoryInlineStore{}
}

// SaveInline records a saved inline row.
func (s *MemoryInlineStore) SaveInline(row map[string]any) error {
	s.Saved = append(s.Saved, cloneRow(row))
	return nil
}

// DeleteInline records a deleted inline row.
func (s *MemoryInlineStore) DeleteInline(row map[string]any) error {
	s.Deleted = append(s.Deleted, cloneRow(row))
	return nil
}
