package admin

import (
	"errors"
	"strings"
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

// DeletionModelCount stores the per-model delete count displayed in confirmations.
type DeletionModelCount struct {
	Name  string
	Count int
}

// DeletionSummary stores delete confirmation context.
type DeletionSummary struct {
	Objects            []string
	Protected          []string
	PermissionsMissing []string
	SelectedIDs        []string
	ModelCounts        []DeletionModelCount
	Count              int
}

// CollectDeletion walks objects and related objects for confirmation.
func CollectDeletion(objects []DeletionObject) DeletionSummary {
	var summary DeletionSummary
	counts := map[string]int{}
	for _, object := range objects {
		collectDeletionObject(object, &summary, counts)
	}
	for _, object := range summary.Objects {
		label, _, _ := strings.Cut(object, ": ")
		if counts[label] > 0 {
			summary.ModelCounts = append(summary.ModelCounts, DeletionModelCount{Name: label, Count: counts[label]})
			counts[label] = 0
		}
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

func collectDeletionObject(object DeletionObject, summary *DeletionSummary, counts map[string]int) {
	label := object.Label + ": " + object.Repr
	summary.Objects = append(summary.Objects, label)
	counts[object.Label]++
	if object.ObjectID != "" {
		summary.SelectedIDs = append(summary.SelectedIDs, object.ObjectID)
	}
	summary.Count++
	if object.Protected {
		summary.Protected = append(summary.Protected, label)
	}
	for _, related := range object.Related {
		collectDeletionObject(related, summary, counts)
	}
}
