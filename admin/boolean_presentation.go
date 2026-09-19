package admin

// Only actual booleans become icons; never call application display methods or
// treat zero, empty strings or placeholders as booleans.
func booleanIcon(value any) string {
	switch value := value.(type) {
	case bool:
		if value {
			return "yes"
		}
		return "no"
	case *bool:
		if value == nil {
			return "unknown"
		}
		return booleanIcon(*value)
	}
	return ""
}
