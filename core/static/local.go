package static

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const ManifestName = "manifest.json"

func canonicalDirectory(name string) (string, error) {
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(name)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(name)
		if parent == name {
			return "", err
		}
		tail = append(tail, filepath.Base(name))
		name = parent
	}
}
func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
func (c Collector) checkRootSeparation() error {
	target, err := canonicalDirectory(c.config.Destination)
	if err != nil {
		return fail(ErrInvalid, err)
	}
	for _, source := range c.config.Sources {
		if source.Directory == "" {
			continue
		}
		origin, err := canonicalDirectory(source.Directory)
		if err != nil {
			return fail(ErrInvalid, err)
		}
		if pathContains(origin, target) || pathContains(target, origin) {
			return ErrInvalid
		}
	}
	return nil
}

// LoadManifest opens only the named local manifest through a rooted directory.
// It does not select an environment, register a provider or open other services.
func LoadManifest(ctx context.Context, directory, baseURL string) (result *Manifest, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = fail(ErrSource, contextError(ctx))
		}
		if err != nil {
			result = nil
			err = fail(ErrSource, err)
		}
	}()
	if !validDirectory(directory) {
		return nil, ErrInvalid
	}
	root, e := os.OpenRoot(directory)
	if e != nil {
		return nil, fail(ErrSource, e)
	}
	defer func() {
		err = errors.Join(err, root.Close())
		if err != nil {
			result = nil
		}
	}()
	if e = contextError(ctx); e != nil {
		return nil, e
	}
	info, e := root.Lstat(ManifestName)
	if e != nil {
		return nil, fail(ErrSource, e)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	file, e := root.Open(ManifestName)
	if e != nil {
		return nil, fail(ErrSource, e)
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			result = nil
		}
	}()
	return ReadManifest(ctx, file, baseURL)
}

func openDestination(name string) (root *os.Root, err error) {
	// Creation remains below a captured descriptor for the configured parent.
	// Missing ancestors must be explicitly prepared by the deployment owner.
	parent, e := os.OpenRoot(filepath.Dir(name))
	if e != nil {
		return nil, fail(ErrPublish, e)
	}
	defer func() {
		err = errors.Join(err, parent.Close())
		if err != nil && root != nil {
			err = errors.Join(err, root.Close())
			root = nil
		}
	}()
	leaf := filepath.Base(name)
	if !validPath(leaf) {
		return nil, ErrInvalid
	}
	info, e := parent.Lstat(leaf)
	if errors.Is(e, fs.ErrNotExist) {
		if e = parent.Mkdir(leaf, 0755); e != nil && !errors.Is(e, fs.ErrExist) {
			return nil, fail(ErrPublish, e)
		}
		info, e = parent.Lstat(leaf)
	}
	if e != nil {
		return nil, fail(ErrPublish, e)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	root, e = parent.OpenRoot(leaf)
	if e != nil {
		return nil, fail(ErrPublish, e)
	}
	return root, nil
}
func randomName() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return ".gogo-static-" + hex.EncodeToString(bytes[:]), nil
}

func (c Collector) publish(ctx context.Context, set preparedSet) (published bool, err error) {
	// Go does not promise atomic Rename on non-Unix platforms. The initial
	// local publisher's tested contract is explicitly Linux and macOS only.
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return false, ErrUnsupported
	}
	root, e := openDestination(c.config.Destination)
	if e != nil {
		return false, e
	}
	defer func() {
		err = errors.Join(err, root.Close())
		if err != nil {
			err = fail(ErrPublish, err)
		}
	}()
	stage, e := randomName()
	if e != nil {
		return false, e
	}
	if e = root.Mkdir(stage, 0700); e != nil {
		return false, e
	}
	var temporary []string
	defer func() {
		// Remove only exact temporary files created by this operation. Promoted
		// immutable files remain safe unreferenced orphans after a failed run.
		for _, name := range temporary {
			if e := root.Remove(name); e != nil && !errors.Is(e, fs.ErrNotExist) {
				err = errors.Join(err, e)
			}
		}
		if e := root.Remove(stage); e != nil {
			err = errors.Join(err, e)
		}
	}()
	names := make([]string, 0, len(set.bytes))
	for name := range set.bytes {
		names = append(names, name)
	}
	sort.Strings(names)
	directories := map[string]bool{".": true}
	for index, name := range names {
		if e = contextError(ctx); e != nil {
			return false, e
		}
		if !validPath(name) {
			return false, ErrInvalid
		}
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			directories[directory] = true
		}
		if e = ensureDirectories(root, path.Dir(name)); e != nil {
			return false, e
		}
		data := set.bytes[name]
		if info, statErr := root.Lstat(name); statErr == nil {
			if !info.Mode().IsRegular() {
				return false, ErrInvalid
			}
			existing, readErr := readAsset(ctx, root.FS(), name, c.config.Limits.MaxFileBytes)
			if readErr != nil {
				return false, readErr
			}
			if digest(existing) != digest(data) {
				return false, ErrPublish
			}
			continue
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return false, statErr
		}
		temp := path.Join(stage, itoa(index))
		file, e := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return false, e
		}
		temporary = append(temporary, temp)
		if e = writeStaticFile(ctx, file, data); e != nil {
			return false, e
		}
		if e = contextError(ctx); e != nil {
			return false, e
		}
		if e = root.Link(temp, name); e != nil {
			if !errors.Is(e, fs.ErrExist) {
				return false, e
			}
			existing, readErr := readAsset(ctx, root.FS(), name, c.config.Limits.MaxFileBytes)
			if readErr != nil {
				return false, readErr
			}
			if digest(existing) != digest(data) {
				return false, ErrPublish
			}
		}
	}
	// Flush file links/directories before publishing a manifest that names them.
	dirs := make([]string, 0, len(directories))
	for directory := range directories {
		dirs = append(dirs, directory)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, directory := range dirs {
		if e = syncDirectory(root, directory); e != nil {
			return false, e
		}
	}
	if info, statErr := root.Lstat(ManifestName); statErr == nil {
		if !info.Mode().IsRegular() {
			return false, ErrInvalid
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return false, statErr
	}
	manifestTemp := path.Join(stage, "manifest")
	file, e := root.OpenFile(manifestTemp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return false, e
	}
	temporary = append(temporary, manifestTemp)
	if e = writeStaticFile(ctx, file, set.manifest.encoded); e != nil {
		return false, e
	}
	if e = contextError(ctx); e != nil {
		return false, e
	}
	if e = root.Rename(manifestTemp, ManifestName); e != nil {
		return false, e
	}
	published = true
	// Cancellation after the successful rename cannot undo the observed swap.
	// A directory-sync error is returned with Published=true for reconciliation.
	if e = syncDirectory(root, "."); e != nil {
		return true, e
	}
	return true, nil
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var bytes [20]byte
	i := len(bytes)
	for n > 0 {
		i--
		bytes[i] = byte('0' + n%10)
		n /= 10
	}
	return string(bytes[i:])
}
func ensureDirectories(root *os.Root, directory string) error {
	if directory == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(directory, "/") {
		current = path.Join(current, part)
		if !validPath(current) {
			return ErrInvalid
		}
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err = root.Mkdir(current, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return ErrInvalid
		}
	}
	return nil
}
func writeStaticFile(ctx context.Context, file *os.File, data []byte) (err error) {
	defer func() { err = errors.Join(err, file.Close()) }()
	if err = contextError(ctx); err != nil {
		return err
	}
	n, err := file.Write(data)
	if n != len(data) {
		err = errors.Join(err, io.ErrShortWrite)
	}
	if err != nil {
		return err
	}
	if err = file.Chmod(0644); err != nil {
		return err
	}
	return file.Sync()
}
func syncDirectory(root *os.Root, name string) (err error) {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return file.Sync()
}
