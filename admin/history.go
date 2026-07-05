package admin

// HistoryPage stores render-ready object history.
type HistoryPage struct {
	ContentType string
	ObjectID    string
	Entries     []AdminLogEntry
}

// BuildHistoryPage filters log entries for one object.
func BuildHistoryPage(store AdminLogStore, contentType, objectID string) HistoryPage {
	page := HistoryPage{ContentType: contentType, ObjectID: objectID}
	if store == nil {
		return page
	}
	entries, err := store.EntriesForObject(contentType, objectID)
	if err != nil {
		return page
	}
	page.Entries = append(page.Entries, entries...)
	return page
}
