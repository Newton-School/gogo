//go:build darwin || linux

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// This is a generated, source-linked standalone consumer, not evidence of
// installing published archives. All writes/processes/listeners are test-owned;
// there are no database, cache, private imports or alternative reload engines.
// The platform restriction covers the actual generated Main signal lifecycle.
func reloadChildEnvironment(entries []string, control, temporary string) []string {
	var result []string
	var originalPath string
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GOGO_") || strings.HasPrefix(key, "RELOAD_TEST_") {
			continue
		}
		switch key {
		case "PATH":
			originalPath = value
			continue
		case "GOENV", "GOWORK", "GOPROXY", "GOSUMDB", "GONOSUMDB", "GOPRIVATE", "GOFLAGS", "GOTOOLCHAIN", "GOROOT", "GOOS", "GOARCH", "TMPDIR":
			continue
		}
		result = append(result, entry)
	}
	return append(result,
		"GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=sum.golang.org", "GOFLAGS=-mod=mod -p=1", "GOTOOLCHAIN=local",
		"GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH, "TMPDIR="+temporary,
		"PATH="+filepath.Join(runtime.GOROOT(), "bin")+string(os.PathListSeparator)+originalPath,
		"RELOAD_TEST_CONTROL="+control,
	)
}

func TestReloadConsumerEnvironmentIsolatesConfigurationAndToolchain(t *testing.T) {
	input := []string{"GOGO_TEST_REQUIRE_SERVICES=1", "GOGO_DATABASE_URL=synthetic-unselected-value", "RELOAD_TEST_CONTROL=other", "GOENV=other", "GOSUMDB=off", "GONOSUMDB=*", "GOPRIVATE=*", "GOWORK=other", "GOFLAGS=-race", "PATH=/usr/bin", "UNRELATED=preserved"}
	before := strings.Join(input, "\n")
	values := map[string]string{}
	for _, entry := range reloadChildEnvironment(input, "owned-control", "owned-temporary") {
		key, value, _ := strings.Cut(entry, "=")
		if _, exists := values[key]; exists {
			t.Fatal("duplicate child environment key", key)
		}
		values[key] = value
		if strings.HasPrefix(key, "GOGO_") || strings.HasPrefix(key, "RELOAD_TEST_") && key != "RELOAD_TEST_CONTROL" {
			t.Fatal("child inherited unrelated application/harness settings", key)
		}
	}
	if strings.Join(input, "\n") != before || values["GOENV"] != "off" || values["GOSUMDB"] != "sum.golang.org" || values["GONOSUMDB"] != "" || values["GOPRIVATE"] != "" || values["GOWORK"] != "off" || values["GOFLAGS"] != "-mod=mod -p=1" || values["RELOAD_TEST_CONTROL"] != "owned-control" || values["UNRELATED"] != "preserved" || !strings.HasPrefix(values["PATH"], filepath.Join(runtime.GOROOT(), "bin")+string(os.PathListSeparator)) {
		t.Fatal("child environment boundary changed")
	}
}

type reloadOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *reloadOutput) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(value)
	const maximum = 1 << 20
	if len(value) > maximum-len(b.data) {
		value = value[:maximum-len(b.data)]
		b.truncated = true
	}
	b.data = append(b.data, value...)
	return n, nil
}
func (b *reloadOutput) snapshot(t *testing.T) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		t.Fatal("reload diagnostics exceeded test capture bound")
	}
	return string(b.data)
}

type reloadProcess struct {
	command *exec.Cmd
	output  *reloadOutput
	done    chan struct{}
	err     error // written before closing done; read only after receiving it
}

func startReloadProcess(t *testing.T, directory string, environment []string, executable string, arguments ...string) *reloadProcess {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir, command.Env = directory, append([]string(nil), environment...)
	output := &reloadOutput{}
	command.Stdout, command.Stderr = output, output
	// Only this direct process is owned. No process-group/tree signaling or
	// HTTP-reported PID killing is used to conceal supervisor cleanup defects.
	command.WaitDelay = 3 * time.Second
	if err := command.Start(); err != nil {
		t.Fatal("cannot start reload consumer", err)
	}
	p := &reloadProcess{command: command, output: output, done: make(chan struct{})}
	go func() { p.err = command.Wait(); close(p.done) }()
	t.Cleanup(func() {
		p.stop(t, false)
		if t.Failed() {
			t.Logf("direct supervisor %d diagnostics:\n%s", command.Process.Pid, output.snapshot(t))
		}
	})
	return p
}

func (p *reloadProcess) stop(t *testing.T, requireCancellation bool) {
	t.Helper()
	select {
	case <-p.done:
	default:
		if err := p.command.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("cannot signal directly owned supervisor %d: %v", p.command.Process.Pid, err)
		}
		select {
		case <-p.done:
		case <-time.After(12 * time.Second):
			_ = p.command.Process.Kill()
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				t.Errorf("direct supervisor %d did not finish bounded Wait", p.command.Process.Pid)
				return
			}
			t.Errorf("supervisor %d exceeded graceful cleanup bound; descendants were not signaled by test", p.command.Process.Pid)
		}
	}
	if requireCancellation {
		// RunServer preserves context.Canceled; management.Run uses exit1.
		var exit *exec.ExitError
		if !errors.As(p.err, &exit) || exit.ExitCode() != 1 {
			t.Errorf("unexpected supervisor cancellation status: %v", p.err)
		}
	}
}

type reloadHarness struct {
	t           *testing.T
	root        string
	control     string
	manager     string
	environment []string
	client      *http.Client
}

func (h *reloadHarness) write(name, contents string) {
	h.t.Helper()
	target := filepath.Join(h.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(contents), 0600); err != nil {
		h.t.Fatal(err)
	}
}
func (h *reloadHarness) run(directory, executable string, arguments ...string) string {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir, command.Env = directory, append([]string(nil), h.environment...)
	command.WaitDelay = 3 * time.Second
	var output reloadOutput
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		h.t.Fatalf("public consumer command %v failed: %v (%v)\n%s", arguments, err, ctx.Err(), output.snapshot(h.t))
	}
	return output.snapshot(h.t)
}
func (h *reloadHarness) wait(description string, check func() bool) {
	h.t.Helper()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-timeout.C:
			h.t.Fatal("timed out waiting for " + description)
		case <-ticker.C:
		}
	}
}
func (h *reloadHarness) event(pid int, name string) string {
	h.t.Helper()
	value, err := os.ReadFile(filepath.Join(h.control, strconv.Itoa(pid)+"."+name))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return string(value)
}

type reloadReply struct {
	PID        int    `json:"pid"`
	Generation string `json:"generation"`
	Template   string `json:"template"`
	Token      string `json:"token"`
	Setting    string `json:"setting"`
}

func (h *reloadHarness) response(address, path string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", "http://"+address+path, nil)
	if err != nil {
		return 0, nil, err
	}
	response, err := h.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	closeErr := response.Body.Close()
	if len(data) > 4096 {
		return 0, nil, errors.New("test response exceeded bound")
	}
	return response.StatusCode, data, errors.Join(readErr, closeErr)
}
func (h *reloadHarness) ready(address, generation, template string, previous int) reloadReply {
	h.t.Helper()
	var reply reloadReply
	h.wait("HTTP generation "+generation, func() bool {
		status, data, err := h.response(address, "/")
		if err != nil || status != 200 || json.Unmarshal(data, &reply) != nil {
			return false
		}
		return reply.Token == filepath.Base(h.control) && reply.Generation == generation && reply.Template == template && reply.PID > 1 && reply.PID != previous
	})
	if h.event(reply.PID, "open") == "" || h.event(reply.PID, "ready") == "" {
		h.t.Fatal("HTTP preceded actual resource/Ready lifecycle")
	}
	return reply
}
func (h *reloadHarness) same(address string, previous reloadReply) {
	h.t.Helper()
	status, data, err := h.response(address, "/")
	var current reloadReply
	if err != nil || status != 200 || json.Unmarshal(data, &current) != nil || current != previous {
		h.t.Fatal("failed build replaced or interrupted healthy server", err, status)
	}
}

func newReloadHarness(t *testing.T) *reloadHarness {
	t.Helper()
	framework, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	parent, control, temporary := t.TempDir(), t.TempDir(), t.TempDir()
	h := &reloadHarness{t: t, root: filepath.Join(parent, "client"), control: control, manager: filepath.Join(parent, "manage-client")}
	h.environment = reloadChildEnvironment(os.Environ(), control, temporary)
	transport := &http.Transport{DisableKeepAlives: true}
	h.client = &http.Client{Transport: transport}
	t.Cleanup(transport.CloseIdleConnections)
	checksums, err := os.ReadFile(filepath.Join(framework, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	providerChecksums, err := os.ReadFile(filepath.Join(framework, "connectors/postgres/go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := filepath.Join(parent, "bootstrap")
	if err := os.Mkdir(bootstrap, 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"go.mod":  fmt.Sprintf("module example.com/reload-bootstrap\n\ngo 1.26.8\nrequire github.com/Newton-School/gogo v0.0.0\nreplace github.com/Newton-School/gogo => %q\n", framework),
		"go.sum":  string(checksums),
		"main.go": "package main\nimport gogo \"github.com/Newton-School/gogo\"\nfunc main(){gogo.Main(gogo.Project{Name:\"reload-bootstrap\"})}\n",
	} {
		if err := os.WriteFile(filepath.Join(bootstrap, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	goBinary := filepath.Join(runtime.GOROOT(), "bin/go")
	bootstrapBinary := filepath.Join(parent, "bootstrap-client")
	h.run(bootstrap, goBinary, "build", "-trimpath", "-o", bootstrapBinary, "main.go")
	h.run(parent, bootstrapBinary, "startproject", "--module=example.com/reload-client", h.root)
	module, err := os.ReadFile(filepath.Join(h.root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	h.write("go.mod", string(module)+fmt.Sprintf("\nreplace github.com/Newton-School/gogo => %q\nreplace github.com/Newton-School/gogo/connectors/postgres => %q\n", framework, filepath.Join(framework, "connectors/postgres")))
	h.write("go.sum", string(checksums)+string(providerChecksums))
	h.run(h.root, bootstrapBinary, "startapp", "catalog")
	h.write("config/settings.go", reloadConsumerSettings)
	h.write("apps/catalog/models.go", reloadConsumerModel("one", false))
	h.write("templates/version.txt", "template-one")
	h.write("assets/public.txt", "explicit public asset")
	h.write("assets/.env", "private asset canary")
	h.write(".env", "# Development test settings\nGOGO_DEBUG=true\nGOGO_SHUTDOWN_GRACE=2s\n")
	h.write(".env.example", "# Development test settings\nGOGO_DEBUG=true\nGOGO_SHUTDOWN_GRACE=2s\n")
	h.run(h.root, goBinary, "build", "-trimpath", "-o", h.manager, "manage.go")
	h.run(h.root, h.manager, "generate")
	h.run(h.root, h.manager, "generate", "--check")
	h.run(h.root, goBinary, "build", "-trimpath", "-o", h.manager, "manage.go")
	return h
}

func TestReloadGeneratedConsumerBuildFailuresDrainAndCancellation(t *testing.T) {
	h := newReloadHarness(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	process := startReloadProcess(t, h.root, h.environment, h.manager, "runserver", "--reload", "--watch=assets", "--addr="+address)
	first := h.ready(address, "one", "template-one", 0)
	if h.event(process.command.Process.Pid, "open") != "" || h.event(process.command.Process.Pid, "ready") != "" {
		t.Fatal("reload supervisor opened child resources")
	}
	if status, body, err := h.response(address, "/assets/public.txt"); err != nil || status != 200 || string(body) != "explicit public asset" {
		t.Fatal("explicit DEBUG static mount unavailable", status, err)
	}
	if status, body, err := h.response(address, "/assets/.env"); err != nil || status != 404 || bytes.Contains(body, []byte("private asset canary")) {
		t.Fatal("private static file exposed", status, err)
	}
	failed := strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes")
	h.write("config/broken.go", "package config\nvar broken = undefinedReloadSymbol\n")
	h.wait("actual compiler error", func() bool {
		output := process.output.snapshot(t)
		return strings.Count(output, "Reload build failed; waiting for changes") > failed && strings.Contains(output, "undefinedReloadSymbol")
	})
	h.same(address, first)
	generated, err := os.ReadFile(filepath.Join(h.root, "apps/catalog/zz_gogo.gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	failed = strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes")
	h.write("apps/catalog/models.go", reloadConsumerModel("two", true))
	h.wait("failed rebuild after model edit", func() bool {
		return strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes") > failed
	})
	h.same(address, first)
	unchanged, err := os.ReadFile(filepath.Join(h.root, "apps/catalog/zz_gogo.gen.go"))
	if err != nil || !bytes.Equal(generated, unchanged) {
		t.Fatal("reload silently rewrote generated descriptors", err)
	}
	failed = strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes")
	if err := os.Remove(filepath.Join(h.root, "config/broken.go")); err != nil {
		t.Fatal(err)
	}
	// The deliberate compiler fault is now absent. The changed declaration is
	// valid Go; only its generated descriptor is stale. A further failed build
	// must leave the same healthy process and descriptor bytes untouched.
	h.wait("stale-only descriptor refusal", func() bool {
		return strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes") > failed
	})
	h.same(address, first)
	unchanged, err = os.ReadFile(filepath.Join(h.root, "apps/catalog/zz_gogo.gen.go"))
	if err != nil || !bytes.Equal(generated, unchanged) {
		t.Fatal("stale-only check rewrote descriptors", err)
	}
	h.run(h.root, h.manager, "generate")
	second := h.ready(address, "two", "template-one", first.PID)
	h.wait("old generation resource close", func() bool { return h.event(first.PID, "closed") == "clean" })
	builds := strings.Count(process.output.snapshot(t), "Reload build started")
	for _, path := range []string{"uploads/ignored.go", ".git/ignored.go", "config/.editor.go.swp"} {
		h.write(path, "not valid Go")
	}
	// A bounded negative observation spans several polling/debounce windows;
	// it is not a claim that arbitrary future filesystem activity is ignored.
	until := time.NewTimer(1200 * time.Millisecond)
	tick := time.NewTicker(100 * time.Millisecond)
	defer until.Stop()
	defer tick.Stop()
ignored:
	for {
		select {
		case <-until.C:
			break ignored
		case <-tick.C:
			h.same(address, second)
		}
	}
	tick.Stop()
	if strings.Count(process.output.snapshot(t), "Reload build started") != builds {
		t.Fatal("ignored directory/editor changes triggered a build")
	}
	h.write("templates/version.txt", "template-two")
	third := h.ready(address, "two", "template-two", second.PID)
	h.write("assets/public.txt", "updated explicit public asset")
	withAssets := h.ready(address, "two", "template-two", third.PID)
	if status, body, err := h.response(address, "/assets/public.txt"); err != nil || status != 200 || string(body) != "updated explicit public asset" {
		t.Fatal("explicit watch directory did not reload public assets", status, err)
	}
	h.write(".env", "# Development test settings\nGOGO_DEBUG=false\nGOGO_SHUTDOWN_GRACE=2s\n")
	h.write(".env.example", "# Development test settings\nGOGO_DEBUG=false\nGOGO_SHUTDOWN_GRACE=2s\n")
	fourth := h.ready(address, "two", "template-two", withAssets.PID)
	if status, _, err := h.response(address, "/assets/public.txt"); err != nil || status != 404 {
		t.Fatal("debug assets survived fresh DEBUG=false settings", status, err)
	}
	// A replacement owns its new configuration schema. Adding a required
	// resource setting must not kill the old child while its value is missing,
	// nor be vetoed by the original supervisor schema once the value is added.
	failed = strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes")
	withDefinition := strings.Replace(reloadConsumerSettings,
		"func Settings()conf.Schema{return conf.CoreSchema()}",
		`func Settings()conf.Schema{return append(conf.CoreSchema(),conf.Definition{Name:"GOGO_RELOAD_LABEL",Group:"Reload fixture",RequiredFor:[]string{"reload_fixture"}})}`, 1)
	if withDefinition == reloadConsumerSettings {
		t.Fatal("dynamic schema fixture replacement did not apply")
	}
	h.write("config/settings.go", withDefinition)
	h.wait("new required setting refusal", func() bool {
		return strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes") > failed
	})
	h.same(address, fourth)
	if h.event(fourth.PID, "closed") != "" {
		t.Fatal("missing candidate setting stopped the healthy child")
	}
	// Preflight must use the same entry snapshot as the eventual server. A
	// Register hook cannot supply a missing setting after that snapshot and
	// trick check/diffsettings into admitting a child that runserver rejects.
	lateEnvironment := strings.Replace(withDefinition, "func Project()management.Project{", "func Project()management.Project{\n environment:=map[string]string{}", 1)
	lateEnvironment = strings.Replace(lateEnvironment, `Name:"reload-client",`, `Name:"reload-client",Environment:environment,`, 1)
	lateEnvironment = strings.Replace(lateEnvironment, "Register:func(r *app.Registry)error{return", `Register:func(r *app.Registry)error{environment["GOGO_RELOAD_LABEL"]="late-hook-value";return`, 1)
	if !strings.Contains(lateEnvironment, `environment["GOGO_RELOAD_LABEL"]="late-hook-value"`) || !strings.Contains(lateEnvironment, "Environment:environment,") {
		t.Fatal("late registration fixture did not apply")
	}
	failed = strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes")
	h.write("config/settings.go", lateEnvironment)
	h.wait("late registration preflight refusal", func() bool {
		if h.event(fourth.PID, "closed") != "" {
			t.Fatal("preflight admitted settings inconsistent with runserver and stopped the healthy child")
		}
		return strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes") > failed
	})
	h.same(address, fourth)
	h.write("config/settings.go", withDefinition)
	configured := "# Development test settings\nGOGO_DEBUG=false\nGOGO_SHUTDOWN_GRACE=2s\n\n# Public reload fixture value\nGOGO_RELOAD_LABEL=new-schema-value\n"
	h.write(".env", configured)
	h.write(".env.example", configured)
	withSchema := h.ready(address, "two", "template-two", fourth.PID)
	if withSchema.Setting != "new-schema-value" {
		t.Fatal("replacement did not load its newly declared setting")
	}
	select {
	case <-process.done:
		t.Fatal("schema transition restarted or lost the original supervisor")
	default:
	}
	h.wait("pre-schema generation close", func() bool { return h.event(fourth.PID, "closed") == "clean" })
	fourth = withSchema
	// A fresh code-defined Environment layer has precedence over .env. The
	// original supervisor has no such override and must not reject the edit
	// by applying its stale declaration before compiling the new candidate.
	withOverride := strings.Replace(withDefinition, `Name:"reload-client",`, `Name:"reload-client",Environment:map[string]string{"GOGO_SHUTDOWN_GRACE":"2s"},`, 1)
	if withOverride == withDefinition {
		t.Fatal("dynamic environment fixture replacement did not apply")
	}
	h.write("config/settings.go", withOverride)
	configured = strings.Replace(configured, "GOGO_SHUTDOWN_GRACE=2s", "GOGO_SHUTDOWN_GRACE=overridden-in-new-code", 1)
	h.write(".env", configured)
	h.write(".env.example", configured)
	fourth = h.ready(address, "two", "template-two", fourth.PID)
	// A fresh configuration may compile and pass generic system checks yet
	// select an unsafe output root. Refuse only that candidate, not the still
	// healthy server and watcher that were validated for the previous config.
	failed = strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes")
	h.write(".env", configured+"GOGO_STORAGE_ROOT=.\n")
	h.wait("unsafe candidate output root refusal", func() bool {
		return strings.Count(process.output.snapshot(t), "Reload build failed; waiting for changes") > failed
	})
	h.same(address, fourth)
	if h.event(fourth.PID, "closed") != "" {
		t.Fatal("unsafe replacement output root stopped the healthy child")
	}
	h.write(".env", configured)
	fourth = h.ready(address, "two", "template-two", fourth.PID)
	slowResult := make(chan error, 1)
	slowContext, cancelSlow := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSlow()
	go func() {
		request, err := http.NewRequestWithContext(slowContext, "GET", "http://"+address+"/slow", nil)
		if err != nil {
			slowResult <- err
			return
		}
		response, err := h.client.Do(request)
		if err != nil {
			slowResult <- err
			return
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
		closeErr := response.Body.Close()
		if response.StatusCode != 200 || string(body) != "slow:two" {
			err = errors.New("old in-flight response was lost or replaced")
		}
		slowResult <- errors.Join(err, readErr, closeErr)
	}()
	h.wait("old request admission", func() bool { return h.event(fourth.PID, "slow-entered") == "two" })
	h.write("apps/catalog/models.go", reloadConsumerModel("three", true))
	h.wait("old listener drain", func() bool {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = connection.Close()
		return false
	})
	if h.event(fourth.PID, "closed") != "" {
		t.Fatal("resources closed before request drain")
	}
	if err := os.WriteFile(filepath.Join(h.control, "release-slow"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-slowResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not finish during grace")
	}
	fifth := h.ready(address, "three", "template-two", fourth.PID)
	if fifth.Setting != "new-schema-value" {
		t.Fatal("later generation lost the candidate-owned setting")
	}
	h.wait("drained resource close", func() bool { return h.event(fourth.PID, "closed") == "drained" })
	process.stop(t, true)
	h.wait("last child resource close", func() bool { return h.event(fifth.PID, "closed") == "clean" })
	h.wait("listener released after supervisor cancellation", func() bool {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = connection.Close()
		return false
	})
}

func reloadConsumerModel(generation string, extra bool) string {
	moreStruct, moreField := "", ""
	if extra {
		moreStruct = "Extra bool"
		moreField = `,models.BooleanField("extra",models.WithStructField("Extra"))`
	}
	return fmt.Sprintf(`package catalog
import "github.com/Newton-School/gogo/core/models"
type Item struct {models.Base; ID int64; Label string; %s}
func(*Item)Schema()models.Schema{return models.Schema{AppLabel:"catalog",Name:"Item",Fields:[]models.Field{models.BigAutoField("id",models.WithStructField("ID")),models.TextField("label",models.WithStructField("Label"))%s}}}
func Generation()string{return %q}
`, moreStruct, moreField, generation)
}

const reloadConsumerSettings = `package config
import (
 "context"
 "encoding/json"
 "errors"
 "net/http"
 "os"
 "path/filepath"
 "strconv"
 "time"
 "example.com/reload-client/apps/catalog"
 "github.com/Newton-School/gogo/core/app"
 "github.com/Newton-School/gogo/core/conf"
 "github.com/Newton-School/gogo/core/management"
 "github.com/Newton-School/gogo/core/static"
)
func Settings()conf.Schema{return conf.CoreSchema()}
func event(name,value string)error{return os.WriteFile(filepath.Join(os.Getenv("RELOAD_TEST_CONTROL"),strconv.Itoa(os.Getpid())+"."+name),[]byte(value),0600)}
func Project()management.Project{
 apps:=append(InstalledApps(),app.Config{Name:"reload.lifetime",Label:"lifetime",Register:func(r *app.Registry)error{return r.Register("reload","generation",catalog.Generation())},Ready:func(context.Context,*app.Registry)error{return event("ready",catalog.Generation())},Shutdown:func(context.Context)error{return event("shutdown",catalog.Generation())}})
 return management.Project{Name:"reload-client",Schema:Settings(),Apps:apps,RuntimeResources:[]string{"reload_fixture"},ResourceFactory:func(_ conf.Values,required []string)([]app.Resource,error){
  if len(required)!=1||required[0]!="reload_fixture"{return nil,errors.New("unexpected fixture resource selection")}
  return []app.Resource{{Name:"reload_fixture",Open:func(context.Context)(func(context.Context)error,error){
   if err:=event("open",catalog.Generation());err!=nil{return nil,err}
   return func(context.Context)error{
    state:="clean"
    prefix:=filepath.Join(os.Getenv("RELOAD_TEST_CONTROL"),strconv.Itoa(os.Getpid()))
    if _,err:=os.Stat(prefix+".slow-entered");err==nil{
     if _,err:=os.Stat(prefix+".slow-finished");err!=nil{_ =event("closed","premature");return errors.New("resource closed before drain")}
     state="drained"
    }
    return event("closed",state)
   },nil
  }}},nil
 },Handler:func(registry *app.Registry,settings conf.Values)(http.Handler,error){
  generation,ok:=registry.Get("reload","generation");if !ok||generation!=catalog.Generation(){return nil,errors.New("registry generation drift")}
  template,err:=os.ReadFile("templates/version.txt");if err!=nil{return nil,err}
  mux:=http.NewServeMux()
  if settings.Bool("GOGO_DEBUG"){
   collector,err:=static.New(static.Config{Sources:[]static.Source{{Owner:"project",Directory:"assets"}},BaseURL:"/assets/"});if err!=nil{return nil,err}
   assets,err:=collector.DevHandler(true);if err!=nil{return nil,err};mux.Handle("/assets/",assets)
  }
  mux.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){
   if r.URL.Path!="/"{http.NotFound(w,r);return}
   _=json.NewEncoder(w).Encode(map[string]any{"pid":os.Getpid(),"generation":generation,"template":string(template),"token":filepath.Base(os.Getenv("RELOAD_TEST_CONTROL")),"setting":settings.String("GOGO_RELOAD_LABEL")})
  })
  mux.HandleFunc("/slow",func(w http.ResponseWriter,r *http.Request){
   if event("slow-entered",catalog.Generation())!=nil{http.Error(w,"fixture failure",500);return}
   ticker:=time.NewTicker(10*time.Millisecond);defer ticker.Stop()
   for{
    if _,err:=os.Stat(filepath.Join(os.Getenv("RELOAD_TEST_CONTROL"),"release-slow"));err==nil{break}
    select{case<-r.Context().Done():return;case<-ticker.C:}
   }
   if _,err:=w.Write([]byte("slow:"+catalog.Generation()));err==nil{_=event("slow-finished",catalog.Generation())}
  })
  return mux,nil
 }}
}
`
