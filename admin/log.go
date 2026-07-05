package admin

import "time"

// ActionFlag identifies an admin log action.
type ActionFlag string

const (
	ActionFlagAddition ActionFlag = "addition"
	ActionFlagChange   ActionFlag = "change"
	ActionFlagDeletion ActionFlag = "deletion"
	ActionFlagAction   ActionFlag = "action"
)

// AdminLogEntry stores one admin history event.
type AdminLogEntry struct {
	ActionTime    time.Time
	UserID        int64
	ContentType   string
	ObjectID      string
	ObjectRepr    string
	ActionFlag    ActionFlag
	ChangeMessage string
}

// MemoryLogStore stores admin log entries in memory.
type MemoryLogStore struct {
	Now     func() time.Time
	Entries []AdminLogEntry
}

// NewMemoryLogStore creates an empty log store.
func NewMemoryLogStore() *MemoryLogStore {
	return &MemoryLogStore{}
}

// Log appends one entry, setting ActionTime when omitted.
func (s *MemoryLogStore) Log(entry AdminLogEntry) error {
	if entry.ActionTime.IsZero() {
		entry.ActionTime = time.Now().UTC()
		if s.Now != nil {
			entry.ActionTime = s.Now().UTC()
		}
	}
	s.Entries = append(s.Entries, entry)
	return nil
}

// EntriesForObject returns entries for one content type and object id.
func (s *MemoryLogStore) EntriesForObject(contentType, objectID string) ([]AdminLogEntry, error) {
	if s == nil {
		return nil, nil
	}
	entries := make([]AdminLogEntry, 0)
	for _, entry := range s.Entries {
		if entry.ContentType == contentType && entry.ObjectID == objectID {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// LogAddition records an added object.
func (s *MemoryLogStore) LogAddition(userID int64, contentType, objectID, objectRepr string) error {
	return s.Log(AdminLogEntry{UserID: userID, ContentType: contentType, ObjectID: objectID, ObjectRepr: objectRepr, ActionFlag: ActionFlagAddition, ChangeMessage: "added"})
}

// LogChange records a changed object.
func (s *MemoryLogStore) LogChange(userID int64, contentType, objectID, objectRepr, message string) error {
	return s.Log(AdminLogEntry{UserID: userID, ContentType: contentType, ObjectID: objectID, ObjectRepr: objectRepr, ActionFlag: ActionFlagChange, ChangeMessage: message})
}

// LogDeletion records a deleted object.
func (s *MemoryLogStore) LogDeletion(userID int64, contentType, objectID, objectRepr string) error {
	return s.Log(AdminLogEntry{UserID: userID, ContentType: contentType, ObjectID: objectID, ObjectRepr: objectRepr, ActionFlag: ActionFlagDeletion, ChangeMessage: "deleted"})
}

// LogAction records a custom admin action.
func (s *MemoryLogStore) LogAction(userID int64, contentType, objectID, objectRepr, message string) error {
	return s.Log(AdminLogEntry{UserID: userID, ContentType: contentType, ObjectID: objectID, ObjectRepr: objectRepr, ActionFlag: ActionFlagAction, ChangeMessage: message})
}
