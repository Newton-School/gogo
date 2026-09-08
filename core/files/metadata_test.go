package files

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestFileMetadataExactSchemaAndValidation(t *testing.T) {
	schema := (&File{}).Schema()
	if schema.Key() != "gogo_files.File" || schema.DBTable() != "gogo_files" || schema.Validate() != nil {
		t.Fatal(schema)
	}
	var names []string
	for _, field := range schema.Fields {
		names = append(names, field.Name)
	}
	if !reflect.DeepEqual(names, []string{"id", "storage_alias", "object_key", "owner_ref", "state", "content_type", "bytes", "checksum", "created_at", "finalized_at"}) {
		t.Fatal(names)
	}
	if len(schema.PKFields()) != 1 || schema.PKFields()[0].Name != "id" || len(schema.Constraints) != 3 || schema.Constraints[0].Name != StorageKeyConstraint || !reflect.DeepEqual(schema.Constraints[0].Fields, []string{"storage_alias", "object_key"}) {
		t.Fatal(schema)
	}
	first := Migrations()
	second := Migrations()
	if len(first) != 1 || first[0].App != "gogo_files" || first[0].Name != "0001_initial" || len(first[0].Operations) != 1 {
		t.Fatal(first)
	}
	first[0].Name = "changed"
	if second[0].Name != "0001_initial" {
		t.Fatal("shared migrations")
	}
	id, err := NewUploadIdentity()
	if err != nil {
		t.Fatal(err)
	}
	now := serviceNow()
	valid := File{ID: id.ID, StorageAlias: "private", ObjectKey: id.Key, OwnerRef: `{"v":1,"model":"files_test.Owner","field":"asset","key":[{"name":"id","value":"1"}]}`, State: Ready, ContentType: "text/plain; charset=utf-8", Bytes: 0, Checksum: strings.Repeat("0", 64), CreatedAt: now, FinalizedAt: &now}
	if err := valid.Clean(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*File){func(f *File) { f.ID = "00000000-0000-0000-0000-000000000000" }, func(f *File) { f.ObjectKey = "../escape" }, func(f *File) { f.StorageAlias = "" }, func(f *File) { f.OwnerRef = "" }, func(f *File) { f.OwnerRef = strings.Repeat("x", MaxOwnerReferenceBytes+1) }, func(f *File) { f.Bytes = -1 }, func(f *File) { f.Checksum = strings.Repeat("A", 64) }, func(f *File) { f.ContentType = "TEXT/PLAIN" }, func(f *File) { f.ContentType = "text/plain\r\nInjected: yes" }, func(f *File) { f.State = "unknown" }, func(f *File) { f.FinalizedAt = nil }, func(f *File) { earlier := now.Add(-1); f.FinalizedAt = &earlier }} {
		file := copyFile(valid)
		mutate(&file)
		if err := file.Clean(context.Background()); err == nil {
			t.Fatal("invalid metadata accepted", file.ID, file.State)
		}
	}
}

func TestFileServiceCanonicalOwnerScalarDoesNotInvokeMethods(t *testing.T) {
	field := fileOwnerSchema().Fields[0]
	for _, value := range []any{fileOwnerStringer{}, &fileOwnerStringer{}, map[string]any{"id": 1}, []int{1}, true, float64(1), "01", "+1"} {
		if _, err := scalarOwnerValue(field, value, true); err == nil {
			t.Fatalf("noncanonical key %T", value)
		}
	}
	for _, value := range []any{int(1), uint32(1), int64(1), "1", []byte("1")} {
		got, err := scalarOwnerValue(field, value, true)
		if err != nil || got != int64(1) {
			t.Fatal(got, err)
		}
	}
}

type fileOwnerStringer struct{}

func (fileOwnerStringer) String() string               { panic("must not call Stringer") }
func (fileOwnerStringer) MarshalJSON() ([]byte, error) { panic("must not call marshaler") }
