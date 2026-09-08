package files

import (
	"context"
	"testing"
)

func FuzzFileOwnerReference(f *testing.F) {
	f.Add(`{"v":1,"model":"files_test.Owner","field":"asset","key":[{"name":"id","value":"1"}]}`)
	f.Add(`{"v":1,"model":"files_test.Owner","field":"asset","key":[{"name":"id","value":"01"}]}`)
	f.Add(`{"v":1,"model":"files_test.Owner","field":"asset","key":[{"name":"id","value":"1"},{"name":"id","value":"2"}]}`)
	f.Add(`{"v":1,"v":2}`)
	f.Fuzz(func(t *testing.T, wire string) {
		if len(wire) > 2*MaxOwnerReferenceBytes {
			t.Skip()
		}
		config, backend, storage := fileServiceConfig(t)
		config.Bindings[0].Authorize = func(context.Context, OwnerAction, OwnerSnapshot) error {
			t.Fatal("reference parsing invoked authority")
			return ErrForbidden
		}
		service := mustFileService(t, config)
		selection, err := service.state.parseOwner(wire)
		if err == nil {
			if selection.reference != wire || selection.binding.name != "asset" || len(selection.key) != 1 {
				t.Fatal("accepted reference changed identity")
			}
			next, err := service.state.selectOwner(OwnerInput{Binding: selection.binding.name, Key: selection.key})
			if err != nil || next.reference != wire {
				t.Fatal("accepted reference cannot round trip canonically")
			}
		}
		if backend.begins != 0 || backend.queries != 0 || backend.writes != 0 || storage.saves != 0 || storage.opens != 0 {
			t.Fatal("parsing or configuration performed effects")
		}
	})
}
