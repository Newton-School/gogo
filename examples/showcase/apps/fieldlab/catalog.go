package fieldlab

// Entry is a JSON-safe inventory row; it never serializes callback functions,
// uploaded bytes, model instances, or arbitrary provider error strings.
type Entry struct {
	Category      string `json:"category"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Demonstration string `json:"demonstration"`
	Limitation    string `json:"limitation,omitempty"`
}

func Catalog() []Entry {
	var entries []Entry
	for _, example := range ModelCases() {
		level := "Valid and invalid field cleaning; schema validation; PostgreSQL type mapping"
		if example.DescriptorOnly {
			level = "Descriptor/schema and backend capability boundary only"
		}
		entries = append(entries, Entry{"model", example.Constructor, string(example.Field.Kind), level, example.Limitation})
	}
	for _, example := range FormCases() {
		entries = append(entries, Entry{"form", "NewField(" + string(example.Field.Kind) + ")", string(example.Field.Kind), "Valid/invalid binding, cleaned values and accessible HTML rendering", example.Limitation})
	}
	for _, example := range WidgetCases() {
		entries = append(entries, Entry{"widget", example.Name, string(example.Field.Kind), "Escaped HTML rendering through public widget APIs", ""})
	}
	entries = append(entries,
		Entry{"limitation", "ClearableFileInput", "not_implemented", "Not implemented by this release's InputWidget", "File input does not imply clear/replace storage lifecycle support."},
		Entry{"limitation", "SelectDateWidget", "not_implemented", "Not implemented by this release's InputWidget", "Date input is supported; a day/month/year select widget is not."},
	)
	return entries
}
