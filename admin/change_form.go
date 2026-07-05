package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cybersaksham/gogo/auth"
	"github.com/cybersaksham/gogo/models"
)

// ChangeFormMode identifies add or edit mode.
type ChangeFormMode string

const (
	ChangeFormAdd  ChangeFormMode = "add"
	ChangeFormEdit ChangeFormMode = "edit"
)

// WidgetKind identifies admin field widgets.
type WidgetKind string

const (
	WidgetText                   WidgetKind = "text"
	WidgetTextarea               WidgetKind = "textarea"
	WidgetNumber                 WidgetKind = "number"
	WidgetSelect                 WidgetKind = "select"
	WidgetDate                   WidgetKind = "date"
	WidgetTime                   WidgetKind = "time"
	WidgetFile                   WidgetKind = "file"
	WidgetReadonly               WidgetKind = "readonly"
	WidgetCheckbox               WidgetKind = "checkbox"
	WidgetDateTime               WidgetKind = "datetime"
	WidgetEmail                  WidgetKind = "email"
	WidgetPasswordHash           WidgetKind = "password_hash"
	WidgetRawID                  WidgetKind = "raw_id"
	WidgetAutocomplete           WidgetKind = "autocomplete"
	WidgetRadio                  WidgetKind = "radio"
	WidgetFilteredSelectMultiple WidgetKind = "filtered_select_multiple"
)

// SaveButton identifies visible submit buttons.
type SaveButton string

const (
	SaveButtonSave              SaveButton = "save"
	SaveButtonSaveAndContinue   SaveButton = "save_continue"
	SaveButtonSaveAndAddAnother SaveButton = "save_add_another"
	SaveButtonSaveAsNew         SaveButton = "save_as_new"
)

// SaveIntent identifies the requested save outcome.
type SaveIntent string

const (
	SaveIntentSave       SaveIntent = "save"
	SaveIntentContinue   SaveIntent = "continue"
	SaveIntentAddAnother SaveIntent = "add_another"
	SaveIntentSaveAsNew  SaveIntent = "save_as_new"
)

// ChangeFormInput configures a change form context build.
type ChangeFormInput struct {
	Mode     ChangeFormMode
	ObjectID string
	User     auth.User
	Request  *http.Request
	Values   map[string]any
}

// ChangeFormContext stores render-ready add/edit metadata.
type ChangeFormContext struct {
	Mode                ChangeFormMode
	ObjectID            string
	Fieldsets           []Fieldset
	Fields              map[string]ChangeFormField
	PrepopulatedFields  map[string][]string
	SaveButtons         []SaveButton
	SaveOnTop           bool
	CanDelete           bool
	DeleteURL           string
	JSI18NURL           string
	Popup               bool
	RawIDFields         []string
	AutocompleteFields  []string
	RadioFields         map[string]string
	FilterHorizontal    []string
	FilterVertical      []string
	RelatedPopupEnabled bool
	ModelLabel          string
	Inlines             []InlineFormset
}

// ChangeFormField describes one rendered form field.
type ChangeFormField struct {
	Name     string
	Widget   WidgetKind
	Readonly bool
	Value    any
	Label    string
	HelpText string
	Required bool
	Choices  []WidgetChoice
	Meta     models.FieldMeta
}

// BuildChangeForm builds add/edit form metadata with permission checks.
func BuildChangeForm(admin ModelAdmin, input ChangeFormInput) (ChangeFormContext, error) {
	user := input.User
	request := input.Request
	if request == nil {
		request, _ = http.NewRequest(http.MethodGet, "/", nil)
	}
	mode := input.Mode
	if mode == "" {
		mode = ChangeFormAdd
	}
	if mode == ChangeFormAdd && !admin.HasAddPermission(request, user) {
		return ChangeFormContext{}, ErrAdminPermissionDenied
	}
	if mode == ChangeFormEdit && !admin.HasChangePermission(request, user) {
		return ChangeFormContext{}, ErrAdminPermissionDenied
	}
	fields := admin.Fields
	if len(fields) == 0 {
		fields = editableModelFields(admin.Model, admin.Exclude)
	}
	fieldsets := cloneFieldsets(admin.Fieldsets)
	if len(fieldsets) == 0 {
		fieldsets = []Fieldset{{Fields: append([]string(nil), fields...)}}
	}
	context := ChangeFormContext{
		Mode:                mode,
		ObjectID:            input.ObjectID,
		Fieldsets:           fieldsets,
		Fields:              buildChangeFormFields(admin, fields, input.Values),
		PrepopulatedFields:  cloneStringSliceMap(admin.PrepopulatedFields),
		SaveButtons:         saveButtons(admin),
		SaveOnTop:           admin.SaveOnTop,
		CanDelete:           mode == ChangeFormEdit && admin.HasDeletePermission(request, user),
		DeleteURL:           deleteURL(input.ObjectID),
		JSI18NURL:           "jsi18n/",
		Popup:               request.URL.Query().Get("_popup") == "1",
		RawIDFields:         append([]string(nil), admin.RawIDFields...),
		AutocompleteFields:  append([]string(nil), admin.AutocompleteFields...),
		RadioFields:         cloneStringMap(admin.RadioFields),
		FilterHorizontal:    append([]string(nil), admin.FilterHorizontal...),
		FilterVertical:      append([]string(nil), admin.FilterVertical...),
		RelatedPopupEnabled: true,
		ModelLabel:          admin.Model.Label(),
	}
	return context, nil
}

func editableModelFields(meta models.Metadata, exclude []string) []string {
	excluded := setFromSlice(exclude)
	fields := make([]string, 0, len(meta.Fields))
	for _, field := range meta.Fields {
		if field.PrimaryKey || field.Name == "" {
			continue
		}
		if field.Editable != nil && !*field.Editable {
			continue
		}
		if hasKey(excluded, field.Name) {
			continue
		}
		fields = append(fields, field.Name)
	}
	if len(fields) == 0 {
		return []string{"__all__"}
	}
	return fields
}

func buildChangeFormFields(admin ModelAdmin, fields []string, values map[string]any) map[string]ChangeFormField {
	readonly := setFromSlice(admin.ReadonlyFields)
	rawID := setFromSlice(admin.RawIDFields)
	autocomplete := setFromSlice(admin.AutocompleteFields)
	radio := setFromSlice(keys(admin.RadioFields))
	filtered := setFromSlice(append(append([]string(nil), admin.FilterHorizontal...), admin.FilterVertical...))
	metaFields := adminFieldMetaMap(admin.Model)
	result := make(map[string]ChangeFormField, len(fields)+len(readonly))
	for _, field := range fields {
		metaField := metaFields[field]
		isReadonly := hasKey(readonly, field)
		result[field] = ChangeFormField{
			Name:     field,
			Widget:   widgetForField(admin.Model.Label(), metaField, field, values[field], readonly, rawID, autocomplete, radio, filtered),
			Readonly: isReadonly,
			Value:    values[field],
			Label:    adminMetadataLabel(metaField),
			HelpText: metaField.HelpText,
			Required: adminMetadataRequired(metaField),
			Choices:  adminWidgetChoices(metaField.Choices),
			Meta:     metaField,
		}
	}
	for field := range readonly {
		if _, ok := result[field]; !ok {
			metaField := metaFields[field]
			result[field] = ChangeFormField{
				Name:     field,
				Widget:   WidgetReadonly,
				Readonly: true,
				Value:    values[field],
				Label:    adminMetadataLabel(metaField),
				HelpText: metaField.HelpText,
				Required: false,
				Choices:  adminWidgetChoices(metaField.Choices),
				Meta:     metaField,
			}
		}
	}
	return result
}

func widgetForField(modelLabel string, metaField models.FieldMeta, field string, value any, readonly, rawID, autocomplete, radio, filtered map[string]struct{}) WidgetKind {
	switch {
	case hasKey(readonly, field):
		return WidgetReadonly
	case metadataKind(metaField) == "password_hash":
		return WidgetPasswordHash
	case hasKey(rawID, field):
		return WidgetRawID
	case hasKey(autocomplete, field):
		return WidgetAutocomplete
	case hasKey(radio, field):
		return WidgetRadio
	case hasKey(filtered, field):
		return WidgetFilteredSelectMultiple
	case len(metaField.Choices) > 0:
		return WidgetSelect
	case metadataKind(metaField) == "boolean":
		return WidgetCheckbox
	case metadataKind(metaField) == "datetime":
		return WidgetDateTime
	case metadataKind(metaField) == "date":
		return WidgetDate
	case metadataKind(metaField) == "time":
		return WidgetTime
	case metadataKind(metaField) == "email":
		return WidgetEmail
	case metadataKind(metaField) == "file" || metadataKind(metaField) == "image" || metadataKind(metaField) == "filepath":
		return WidgetFile
	case metadataKind(metaField) == "integer" || metadataKind(metaField) == "big_integer" || metadataKind(metaField) == "small_integer" || metadataKind(metaField) == "positive_integer" || metadataKind(metaField) == "positive_big_integer" || metadataKind(metaField) == "positive_small_integer" || metadataKind(metaField) == "float" || metadataKind(metaField) == "decimal":
		return WidgetNumber
	case isBooleanAdminField(field, value):
		return WidgetCheckbox
	case isDateTimeAdminField(field, value):
		return WidgetDateTime
	case field == "email":
		return WidgetEmail
	default:
		return WidgetText
	}
}

func adminFieldMetaMap(meta models.Metadata) map[string]models.FieldMeta {
	fields := make(map[string]models.FieldMeta, len(meta.Fields))
	for _, field := range meta.Fields {
		fields[field.Name] = field
	}
	return fields
}

func metadataKind(field models.FieldMeta) string {
	if field.RelationType != "" {
		return strings.ToLower(field.RelationType)
	}
	return strings.ToLower(field.Kind)
}

func adminMetadataLabel(field models.FieldMeta) string {
	return strings.TrimSpace(field.VerboseName)
}

func adminMetadataRequired(field models.FieldMeta) bool {
	if field.Name == "" || field.PrimaryKey || field.Null || field.Blank || field.Default != nil {
		return false
	}
	defaultValue, err := models.NormalizeDatabaseDefault(field.DBDefault)
	return err != nil || defaultValue.Kind == models.DefaultNone
}

func adminWidgetChoices(choices []models.FieldChoiceMeta) []WidgetChoice {
	if len(choices) == 0 {
		return nil
	}
	copied := make([]WidgetChoice, len(choices))
	for i, choice := range choices {
		copied[i] = WidgetChoice{Value: fmt.Sprint(choice.Value), Label: choice.Label}
	}
	return copied
}

func isBooleanAdminField(field string, value any) bool {
	if _, ok := value.(bool); ok {
		return true
	}
	switch field {
	case "is_active", "is_staff", "is_superuser", "enabled":
		return true
	default:
		return false
	}
}

func isDateTimeAdminField(field string, value any) bool {
	if _, ok := value.(time.Time); ok {
		return true
	}
	switch field {
	case "last_login", "date_joined", "created_at", "updated_at", "expire_date":
		return true
	default:
		return false
	}
}

func saveButtons(admin ModelAdmin) []SaveButton {
	buttons := []SaveButton{SaveButtonSave, SaveButtonSaveAndContinue, SaveButtonSaveAndAddAnother}
	if admin.SaveAs {
		buttons = append(buttons, SaveButtonSaveAsNew)
	}
	return buttons
}

// ResolveSaveIntent parses admin submit button intent.
func ResolveSaveIntent(values url.Values) SaveIntent {
	switch {
	case values.Get("_continue") != "":
		return SaveIntentContinue
	case values.Get("_addanother") != "":
		return SaveIntentAddAnother
	case values.Get("_saveasnew") != "":
		return SaveIntentSaveAsNew
	default:
		return SaveIntentSave
	}
}

// RelatedPopup stores add/change related popup response metadata.
type RelatedPopup struct {
	Action     string
	ObjectID   string
	ObjectRepr string
}

// RelatedPopupResponse returns metadata consumed by admin popup JavaScript.
func RelatedPopupResponse(objectID, objectRepr string) RelatedPopup {
	return RelatedPopup{Action: "change", ObjectID: objectID, ObjectRepr: objectRepr}
}

// JavaScriptCatalogResponse stores admin widget translation JavaScript.
type JavaScriptCatalogResponse struct {
	ContentType string
	Body        string
}

// JavaScriptCatalog renders a tiny admin JavaScript translation catalog.
func JavaScriptCatalog(messages map[string]string) JavaScriptCatalogResponse {
	body, _ := json.Marshal(messages)
	return JavaScriptCatalogResponse{ContentType: "application/javascript", Body: `window.gogoAdminCatalog=` + string(body) + `;
function gettext(msgid) {
  const value = window.gogoAdminCatalog[msgid];
  return typeof value === "undefined" ? msgid : value;
}
function gettext_noop(msgid) {
  return msgid;
}
function pgettext(context, msgid) {
  return gettext(msgid);
}
function ngettext(singular, plural, count) {
  return count === 1 ? gettext(singular) : gettext(plural);
}
function npgettext(context, singular, plural, count) {
  return ngettext(singular, plural, count);
}
function interpolate(fmt, obj, named) {
  if (named) {
    return fmt.replace(/%\((\w+)\)s/g, function(match, key) {
      return String(obj[key]);
    });
  }
  let index = 0;
  return fmt.replace(/%s/g, function() {
    return String(obj[index++]);
  });
}
function get_format(formatType) {
  const formats = {
    DATE_INPUT_FORMATS: ["%Y-%m-%d"],
    TIME_INPUT_FORMATS: ["%H:%M:%S"],
    DATETIME_INPUT_FORMATS: ["%Y-%m-%d %H:%M:%S"],
    FIRST_DAY_OF_WEEK: 0
  };
  return typeof formats[formatType] === "undefined" ? formatType : formats[formatType];
}
`}
}

func deleteURL(objectID string) string {
	if objectID == "" {
		return ""
	}
	return objectID + "/delete/"
}

func hasKey(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func keys(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}
