package management

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/internal/codegen"
)

const reloadPoll = 200 * time.Millisecond
const reloadDebounce = 200 * time.Millisecond
const reloadBuildTimeout = 2 * time.Minute

type reloadOutput struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w reloadOutput) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() {
		if recover() != nil {
			n, err = 0, errors.New("server output callback panicked")
		}
	}()
	n, err = w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

type reloadBuild struct {
	done   chan struct{}
	cancel context.CancelFunc
	result reloadCandidate
}
type reloadCandidate struct {
	name     string
	settings conf.Values
	err      error
}

func runReload(ctx context.Context, i Invocation, options RunServerOptions, entry *runServerEntry) (err error) {
	if _, err = reloadGrace(i.Settings); err != nil {
		return err
	}
	watch, err := newReloadWatcher(i.Project.Root, options.WatchDirectories, []string{i.Settings.String("GOGO_STORAGE_ROOT"), i.Settings.String("GOGO_STATIC_ROOT")})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, watch.root.Close()) }()
	tool, err := reloadGoTool(entry.environment)
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", "gogo-reload-")
	if err != nil {
		return err
	}
	// This path comes only from our successful MkdirTemp, never from a project
	// setting or callback. No user directory is recursively removed.
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }()
	var outputLock sync.Mutex
	i.Stdout = reloadOutput{&outputLock, i.Stdout}
	i.Stderr = reloadOutput{&outputLock, i.Stderr}
	var child *reloadProcess
	var build *reloadBuild
	var activeFile string
	grace, _ := reloadGrace(i.Settings)
	defer func() {
		if build != nil {
			build.cancel()
			// Owned build execution has its own finite tool/pipe deadlines. The
			// descriptor checker is cooperative local source work, not app code.
			select {
			case <-build.done:
				err = errors.Join(err, build.result.err)
			case <-time.After(2*reloadWaitDelay + time.Second):
				err = errors.Join(err, errors.New("build cleanup did not complete"))
			}
		}
		err = errors.Join(err, child.stop(grace))
	}()
	observed, err := watch.snapshot(ctx)
	if err != nil {
		return err
	}
	pending, changedAt, sequence := true, time.Time{}, uint64(0)
	watchFailed := false
	ticker := time.NewTicker(reloadPoll)
	defer ticker.Stop()
	for {
		if err = reloadContextError(ctx); err != nil {
			return err
		}
		if pending && build == nil && !watchFailed && (changedAt.IsZero() || time.Since(changedAt) >= reloadDebounce) {
			if err = reloadMessage(i.Stdout, "Reload build started"); err != nil {
				return err
			}
			sequence++
			buildCtx, cancel := context.WithTimeout(ctx, reloadBuildTimeout)
			build = &reloadBuild{done: make(chan struct{}), cancel: cancel}
			current := build
			candidate := filepath.Join(temp, fmt.Sprintf("manage-%d", sequence))
			go func() {
				defer close(current.done)
				defer cancel()
				defer func() {
					if recover() != nil {
						current.result = reloadCandidate{name: candidate, err: errors.New("reload build callback panicked")}
					}
				}()
				current.result = buildReloadCandidate(buildCtx, watch, i, entry, tool, candidate)
			}()
			pending = false
		}
		var built <-chan struct{}
		if build != nil {
			built = build.done
		}
		var childExited <-chan struct{}
		if child != nil {
			childExited = child.done
		}
		select {
		case <-ctx.Done():
			if err = reloadContextError(ctx); err != nil {
				return err
			}
			return errors.New("invalid server cancellation")
		case <-ticker.C:
			latest, snapshotErr := watch.snapshot(ctx)
			if snapshotErr != nil {
				if !watchFailed {
					if err = reloadMessage(i.Stderr, "Reload source check failed; retaining current child"); err != nil {
						return err
					}
				}
				watchFailed = true
				continue
			}
			if latest != observed || watchFailed {
				observed, pending, changedAt = latest, true, time.Now()
			}
			watchFailed = false
		case <-childExited:
			if child.err != nil {
				return child.err
			}
			child = nil
			if activeFile != "" {
				if e := os.Remove(activeFile); e != nil {
					return e
				}
				activeFile = ""
			}
			if err = reloadMessage(i.Stderr, "Reload child exited; waiting for changes"); err != nil {
				return err
			}
		case <-built:
			candidate := build.result
			build.cancel()
			build = nil
			latest, snapshotErr := watch.snapshot(ctx)
			if err = reloadContextError(ctx); err != nil {
				return err
			}
			if snapshotErr != nil || latest != observed {
				pending, changedAt = true, time.Now()
			}
			if snapshotErr == nil {
				observed = latest
			}
			if candidate.err != nil || pending || snapshotErr != nil {
				if e := os.Remove(candidate.name); e != nil && !errors.Is(e, os.ErrNotExist) {
					return e
				}
				if candidate.err != nil {
					if err = reloadMessage(i.Stderr, "Reload build failed; waiting for changes"); err != nil {
						return err
					}
				}
				continue
			}
			rejectCandidate := func() error {
				if e := os.Remove(candidate.name); e != nil && !errors.Is(e, os.ErrNotExist) {
					return e
				}
				return reloadMessage(i.Stderr, "Reload build failed; waiting for changes")
			}
			updated, e := newReloadWatcher(i.Project.Root, options.WatchDirectories, []string{candidate.settings.String("GOGO_STORAGE_ROOT"), candidate.settings.String("GOGO_STATIC_ROOT")})
			if e != nil {
				// Invalid replacement settings do not invalidate the healthy
				// process or its existing watcher. Retry only after another edit.
				if err = rejectCandidate(); err != nil {
					return err
				}
				continue
			}
			if !slices.Equal(updated.excluded, watch.excluded) {
				baseline, checkErr := updated.snapshot(ctx)
				if checkErr != nil {
					if e = updated.root.Close(); e != nil {
						return e
					}
					if err = rejectCandidate(); err != nil {
						return err
					}
					continue
				}
				if e = watch.root.Close(); e != nil {
					_ = updated.root.Close()
					return e
				}
				watch = updated
				observed, pending, changedAt = baseline, true, time.Now()
				if e = os.Remove(candidate.name); e != nil {
					return e
				}
				continue
			}
			if e = updated.root.Close(); e != nil {
				return e
			}
			newGrace, e := reloadGrace(candidate.settings)
			if e != nil {
				return e
			}
			if e = watch.sameDirectory(); e != nil {
				return e
			}
			if e = child.stop(grace); e != nil {
				return e
			}
			if child != nil && child.forced {
				if e = reloadMessage(i.Stderr, "Reload child forcibly stopped after shutdown deadline"); e != nil {
					return e
				}
			}
			child = nil
			if activeFile != "" {
				if e = os.Remove(activeFile); e != nil {
					return e
				}
				activeFile = ""
			}
			if err = reloadContextError(ctx); err != nil {
				return err
			}
			// Shutdown hooks can edit watched files. Do not launch a candidate
			// that became obsolete while the old process drained.
			latest, e = watch.snapshot(ctx)
			if e != nil || latest != observed {
				if removeErr := os.Remove(candidate.name); removeErr != nil {
					return removeErr
				}
				if e == nil {
					observed = latest
				}
				pending, changedAt, watchFailed = true, time.Now(), e != nil
				continue
			}
			command := exec.Command(candidate.name, "runserver", "--addr", options.Address)
			command.Dir, command.Env = i.Project.Root, slices.Clone(entry.environment)
			command.Stdout, command.Stderr = i.Stdout, i.Stderr
			if err = reloadContextError(ctx); err != nil {
				return err
			}
			child, e = startReloadProcess(command)
			if e != nil {
				return e
			}
			activeFile, grace = candidate.name, newGrace
			if err = reloadMessage(i.Stdout, "Reload child started"); err != nil {
				return err
			}
		}
	}
}

func buildReloadCandidate(ctx context.Context, watch *reloadWatcher, i Invocation, entry *runServerEntry, tool, output string) (result reloadCandidate) {
	result.name = output
	err := checkReloadEnvironment(ctx, watch)
	if err != nil {
		result.err = err
		return
	}
	if err = watch.sameDirectory(); err == nil {
		err = codegen.CheckGenerated(i.Project.Root)
	}
	if err != nil {
		result.err = err
		return
	}
	if err = reloadContextError(ctx); err != nil {
		result.err = err
		return
	}
	if err = watch.sameDirectory(); err != nil {
		result.err = err
		return
	}
	command := exec.Command(tool, "build", "-trimpath", "-o", output, "manage.go")
	command.Dir, command.Env = i.Project.Root, slices.Clone(entry.environment)
	command.Stdout, command.Stderr = i.Stdout, i.Stderr
	process, err := startReloadProcess(command)
	if err != nil {
		result.err = err
		return
	}
	process.interrupt = true
	defer func() { result.err = errors.Join(result.err, process.stop(reloadWaitDelay)) }()
	select {
	case <-process.done:
		result.err = process.err
	case <-ctx.Done():
		result.err = reloadContextError(ctx)
	}
	if result.err == nil {
		result.err = reloadContextError(ctx)
	}
	if result.err == nil {
		result.settings, result.err = validateReloadCandidate(ctx, output, i, entry)
	}
	return
}

func checkReloadEnvironment(ctx context.Context, watch *reloadWatcher) error {
	if err := reloadContextError(ctx); err != nil {
		return err
	}
	_, err := readReloadEnvironment(watch.root)
	if err != nil {
		return err
	}
	// Only syntax and filesystem safety are checked in the old process. The
	// compiled candidate owns the new schema, defaults and Environment layer;
	// its preflight must validate those before any old child is stopped.
	return reloadContextError(ctx)
}

func readReloadEnvironment(root *os.Root) (map[string]string, error) {
	env := map[string]string{}
	info, err := root.Lstat(".env")
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, errors.New("unsupported environment file")
		}
		file, e := root.Open(".env")
		if e != nil {
			return nil, e
		}
		opened, e := file.Stat()
		if e != nil || !os.SameFile(info, opened) {
			_ = file.Close()
			return nil, errors.New("environment changed during open")
		}
		reader := &io.LimitedReader{R: file, N: (1 << 20) + 1}
		env, e = conf.ReadEnv(reader)
		e = errors.Join(e, file.Close())
		if e != nil {
			return nil, e
		}
		if reader.N == 0 {
			return nil, errors.New("environment exceeds limit")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return env, nil
}

func reloadGoTool(environment []string) (string, error) {
	for _, directory := range filepath.SplitList(conf.Environment(environment)["PATH"]) {
		// Never execute an implicit current-directory tool or re-read a mutable
		// global PATH after project/context callbacks.
		if !filepath.IsAbs(directory) {
			continue
		}
		candidate := filepath.Join(directory, "go")
		if resolved, err := exec.LookPath(candidate); err == nil && !strings.ContainsRune(resolved, 0) {
			return resolved, nil
		}
	}
	return "", errors.New("Go compiler is unavailable")
}
