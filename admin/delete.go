package admin

import (
	"errors"
)

var ErrProtectedRelation = errors.New("protected relation prevents deletion")

// DeletionObject describes an object considered by the deletion collector.
type DeletionObject struct {
	Label     string
	ObjectID  string
	Repr      string
	Protected bool
	Related   []DeletionObject
}

// DeletionSummary stores delete confirmation context.
type DeletionSummary struct {
	Objects            []string
	Protected          []string
	PermissionsMissing []string
	SelectedIDs        []string
	Count              int
}

// CollectDeletion walks objects and related objects for confirmation.
func CollectDeletion(objects []DeletionObject) DeletionSummary {
	var summary DeletionSummary
	for _, object := range objects {
		collectDeletionObject(object, &summary)
	}
	return summary
}

// ConfirmDeletion rejects protected deletion summaries.
func ConfirmDeletion(summary DeletionSummary) error {
	if len(summary.Protected) > 0 {
		return ErrProtectedRelation
	}
	return nil
}

func collectDeletionObject(object DeletionObject, summary *DeletionSummary) {
	label := object.Label + ": " + object.Repr
	summary.Objects = append(summary.Objects, label)
	if object.ObjectID != "" {
		summary.SelectedIDs = append(summary.SelectedIDs, object.ObjectID)
	}
	summary.Count++
	if object.Protected {
		summary.Protected = append(summary.Protected, label)
	}
	for _, related := range object.Related {
		collectDeletionObject(related, summary)
	}
}
