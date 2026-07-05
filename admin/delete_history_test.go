package admin

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDeletionCollectorSummarizesAndBlocksProtectedRelations(t *testing.T) {
	summary := CollectDeletion([]DeletionObject{
		{Label: "blog.Post", ObjectID: "1", Repr: "Post 1", Related: []DeletionObject{
			{Label: "blog.Comment", ObjectID: "10", Repr: "Comment 10"},
			{Label: "billing.Invoice", ObjectID: "7", Repr: "Invoice 7", Protected: true},
		}},
	})
	if summary.Count != 3 {
		t.Fatalf("summary count = %d", summary.Count)
	}
	if !reflect.DeepEqual(summary.Objects, []string{"blog.Post: Post 1", "blog.Comment: Comment 10", "billing.Invoice: Invoice 7"}) {
		t.Fatalf("objects = %#v", summary.Objects)
	}
	if !reflect.DeepEqual(summary.Protected, []string{"billing.Invoice: Invoice 7"}) {
		t.Fatalf("protected = %#v", summary.Protected)
	}
	if !reflect.DeepEqual(summary.ModelCounts, []DeletionModelCount{
		{Name: "blog.Post", Count: 1},
		{Name: "blog.Comment", Count: 1},
		{Name: "billing.Invoice", Count: 1},
	}) {
		t.Fatalf("model counts = %#v", summary.ModelCounts)
	}
	if err := ConfirmDeletion(summary); !errors.Is(err, ErrProtectedRelation) {
		t.Fatalf("ConfirmDeletion() error = %v, want ErrProtectedRelation", err)
	}
}

func TestAdminLogEntriesAndHistoryPage(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	store := NewMemoryLogStore()
	store.Now = func() time.Time { return now }

	entry := AdminLogEntry{
		UserID:        1,
		ContentType:   "blog.post",
		ObjectID:      "42",
		ObjectRepr:    "Gogo",
		ActionFlag:    ActionFlagAddition,
		ChangeMessage: "created",
	}
	if err := store.Log(entry); err != nil {
		t.Fatalf("Log(addition) error = %v", err)
	}
	store.Now = func() time.Time { return now.Add(time.Minute) }
	_ = store.LogChange(1, "blog.post", "42", "Gogo", "changed title")
	_ = store.LogDeletion(1, "blog.post", "42", "Gogo")
	_ = store.LogAction(1, "blog.post", "42", "Gogo", "published")

	history := BuildHistoryPage(store, "blog.post", "42")
	if len(history.Entries) != 4 {
		t.Fatalf("history entries = %#v", history.Entries)
	}
	if history.Entries[0].ActionFlag != ActionFlagAddition || history.Entries[1].ActionFlag != ActionFlagChange || history.Entries[2].ActionFlag != ActionFlagDeletion || history.Entries[3].ActionFlag != ActionFlagAction {
		t.Fatalf("action flags = %#v", history.Entries)
	}
	if !history.Entries[0].ActionTime.Equal(now) || history.Entries[3].ChangeMessage != "published" {
		t.Fatalf("history content = %#v", history.Entries)
	}
}
