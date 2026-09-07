package admin

import (
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestInlineRelationRowsSeparateAuthorizationState(t *testing.T) {
	_, _, options := ordinaryRelationSite(t)
	base := &toOneSelectState{options: options, names: []string{"parent"}, allowed: map[string]map[string]bool{"parent": {"1": true}}, checked: map[string]toOneReference{"parent": {ObjectID: "original"}}}
	first, second := inlineSelectionRow(base, nil), inlineSelectionRow(base, nil)
	readonly := inlineSelectionRow(base, []string{"parent"})
	first.checked["parent"] = toOneReference{ObjectID: "first"}
	first.names[0] = "changed"
	if len(readonly.names) != 0 || len(second.checked) != 0 || second.names[0] != "parent" || base.checked["parent"].ObjectID != "original" || base.names[0] != "parent" {
		t.Fatal("row selection authorization leaked between rows", first, second, readonly, base)
	}
}

func TestInlineRelationOwnerSnapshotsDoNotFollowLaterMutation(t *testing.T) {
	parentModel := &testRecord{ID: 1, Tenant: "one"}
	record, err := models.Bind(parentModel)
	if err != nil {
		t.Fatal(err)
	}
	state := &inlineState{config: Inline{Schema: (&relationSource{}).Schema(), FKName: "parent"}}
	if err := prepareInlineParentLinks(Object{Record: record}, []*inlineState{state}); err != nil {
		t.Fatal(err)
	}
	parentModel.ID = 2
	if state.parentSnapshot.ID != "1" || state.parentSnapshot.Encoded != "1" || state.parentValue != int64(1) {
		t.Fatal("owner identity was not frozen", state.parentSnapshot, state.parentValue)
	}
}
