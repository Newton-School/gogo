//go:build darwin || linux

package files_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Newton-School/gogo/core/files"
)

func Example_storageNewLocal() {
	// An application normally configures an existing private directory. This
	// executable example owns a disposable directory instead.
	directory, err := os.MkdirTemp("", "gogo-storage-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(directory)
	storage, err := files.NewLocal(files.LocalConfig{Directory: directory})
	if err != nil {
		panic(err)
	}
	defer storage.Close()
	key, err := files.NewKey()
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	result, err := storage.Save(ctx, key, strings.NewReader("private content"))
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Published, result.Bytes)
	_, err = storage.URL(ctx, key)
	fmt.Println(errors.Is(err, files.ErrStorageCapabilityUnavailable))
	if err := storage.Delete(ctx, key); err != nil {
		panic(err)
	}
	// Output:
	// true 15
	// true
}
