package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Command consumers must not inherit integration harness GOGO_* variables:
// application configuration deliberately rejects even unknown empty settings.
// These child-only overrides leave the parent process and its services intact.
func openAPICommandEnvironment(entries []string) []string {
	result := make([]string, 0, len(entries)+6)
	for _, entry := range entries {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GOGO_") || strings.HasPrefix(key, "OPENAPI_COMMAND_") {
			continue
		}
		switch key {
		case "GOWORK", "GOPROXY", "GOSUMDB", "GONOSUMDB", "GOPRIVATE", "GOFLAGS", "GOTOOLCHAIN", "GOOS", "GOARCH":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "GOWORK=off", "GOPROXY=off", "GOSUMDB=sum.golang.org", "GOFLAGS=-mod=mod -p=1", "GOTOOLCHAIN=local", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
}

func TestOpenAPICommandChildEnvironmentIsolatesStrictSettings(t *testing.T) {
	input := []string{"GOGO_TEST_POSTGRES_DSN=not-a-connection", "GOGO_TEST_REQUIRE_SERVICES=1", "GOGO_UNUSED=", "GOGO_SECRET_KEY=not-a-secret", "OPENAPI_COMMAND_MODE=opaque", "GOWORK=elsewhere", "GOSUMDB=off", "GONOSUMDB=*", "GOFLAGS=-race", "UNRELATED=preserved"}
	copy := append([]string(nil), input...)
	output := openAPICommandEnvironment(input)
	values := map[string]string{}
	for _, entry := range output {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GOGO_") || strings.HasPrefix(key, "OPENAPI_COMMAND_") {
			t.Fatal("child inherited a project or harness setting", key)
		}
		if _, duplicate := values[key]; duplicate {
			t.Fatal("duplicate child setting", key)
		}
		values[key] = value
	}
	if !reflect.DeepEqual(copy, input) || values["UNRELATED"] != "preserved" || values["GOWORK"] != "off" || values["GOPROXY"] != "off" || values["GOSUMDB"] != "sum.golang.org" || values["GONOSUMDB"] != "" || values["GOFLAGS"] != "-mod=mod -p=1" {
		t.Fatal("child isolation changed unrelated state", values)
	}
}

func TestOpenAPICommandExternalManageConsumer(t *testing.T) {
	// Actual PostgreSQL document/handler pairing is covered by APIOpenAPI
	// integration tests. This separate compiled client must generate without a
	// database call, so its backend and authorization boundaries are tripwires.
	// Its explicit test resource records management selection/lifetime, not a
	// fake SQL transport or an alternate schema emitter.
	// A local module replacement makes this a source-linked external consumer,
	// not proof of independently installing the published module archives.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	source, err := format.Source([]byte(openAPICommandConsumer))
	if err != nil {
		t.Fatal(err)
	}
	module := fmt.Sprintf("module example.com/openapi-command-client\n\ngo 1.26.0\n\nrequire github.com/Newton-School/gogo v0.0.0\n\nreplace github.com/Newton-School/gogo => %q\n", root)
	// Reuse the repository's verified dependency checksums while resolving only
	// already cached sources. Missing dependencies fail; verification stays on.
	checksums, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{"go.mod": []byte(module), "go.sum": checksums, "manage.go": source} {
		if err := os.WriteFile(filepath.Join(directory, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	baseEnv := openAPICommandEnvironment(os.Environ())
	sequence := 0
	run := func(executable string, args []string, configured bool, mode string) ([]byte, string, int, []string) {
		t.Helper()
		sequence++
		trace := filepath.Join(directory, fmt.Sprintf("events-%d", sequence))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, executable, args...)
		command.Dir = directory
		command.Env = append(append([]string(nil), baseEnv...), "OPENAPI_COMMAND_TRACE="+trace, "OPENAPI_COMMAND_MODE="+mode)
		if configured {
			command.Env = append(command.Env, "GOGO_SCHEMA_VALUE=synthetic-private-setting")
		}
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if ctx.Err() != nil {
			t.Fatalf("compiled consumer exceeded its bound: %v", ctx.Err())
		}
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal("cannot execute compiled consumer", err)
			}
		}
		data, err := os.ReadFile(trace)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		var events []string
		if len(data) > 0 {
			events = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		}
		return stdout.Bytes(), stderr.String(), code, events
	}
	binary := filepath.Join(directory, "manage-client")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if stdout, stderr, code, events := run("go", []string{"build", "-o", binary, "manage.go"}, false, ""); code != 0 || len(stdout) != 0 || stderr != "" || len(events) != 0 {
		t.Fatalf("external consumer build: code=%d stdout=%s stderr=%s events=%v", code, stdout, stderr, events)
	}
	want, stderr, code, events := run(binary, []string{"direct"}, false, "")
	if code != 0 || stderr != "" || len(events) != 0 || len(want) == 0 {
		t.Fatalf("direct public OpenAPI: code=%d stderr=%s events=%v", code, stderr, events)
	}
	document := openAPINativeJSON(t, want)
	if document["openapi"] != "3.1.1" || bytes.Contains(want, []byte("private_title")) {
		t.Fatal("unexpected direct public document", string(want))
	}
	wantEvents := []string{"select:metadata", "open", "ready", "resolve", "shutdown", "close"}
	for _, invocation := range []struct {
		name string
		args []string
	}{{binary, []string{"openapi"}}, {"go", []string{"run", "manage.go", "openapi"}}} {
		stdout, stderr, code, events := run(invocation.name, invocation.args, true, "")
		if code != 0 || stderr != "" || !bytes.Equal(stdout, want) || !reflect.DeepEqual(events, wantEvents) {
			t.Fatalf("central command differs from direct generator: %v code=%d stderr=%s events=%v stdout=%s", invocation.args, code, stderr, events, stdout)
		}
	}
	for _, test := range []struct {
		name       string
		args       []string
		configured bool
		mode       string
		code       int
		opened     bool
	}{
		{"missing required resource setting", []string{"openapi"}, false, "", 1, false},
		{"help before configuration", []string{"openapi", "--help"}, false, "", 0, false},
		{"unknown flag before configuration", []string{"openapi", "--unknown"}, false, "", 2, false},
		{"extra positional before configuration", []string{"openapi", "extra"}, false, "", 2, false},
		{"opaque route contract", []string{"openapi"}, true, "opaque", 1, true},
		{"missing custom output contract", []string{"openapi"}, true, "malformed", 1, true},
		{"private resolver error", []string{"openapi"}, true, "error", 1, true},
		{"private resolver panic", []string{"openapi"}, true, "panic", 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, code, events := run(binary, test.args, test.configured, test.mode)
			if code != test.code || len(stdout) != 0 || stderr == "" || strings.Contains(stderr, "synthetic-private-setting") || strings.Contains(stderr, "private-resolver-cause") {
				t.Fatalf("unsafe command outcome: code=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			if test.opened {
				if !reflect.DeepEqual(events, wantEvents) {
					t.Fatal("failed generation did not close its selected resource", events)
				}
			} else if len(events) != 0 {
				t.Fatal("invalid or help invocation opened resources", events)
			}
		})
	}
}

const openAPICommandConsumer = `package main

import (
 "context"
 "errors"
 "fmt"
 "io"
 "net/http"
 "os"
 "reflect"

 "github.com/Newton-School/gogo/core/api"
 "github.com/Newton-School/gogo/core/app"
 "github.com/Newton-School/gogo/core/auth"
 "github.com/Newton-School/gogo/core/conf"
 "github.com/Newton-School/gogo/core/db"
 "github.com/Newton-School/gogo/core/management"
 "github.com/Newton-School/gogo/core/models"
 "github.com/Newton-School/gogo/core/orm"
 "github.com/Newton-School/gogo/core/urls"
)

type noQueryBackend struct { db.Backend }
func (*noQueryBackend) Alias() string { panic("schema generation called backend") }
func (*noQueryBackend) Dialect() db.Dialect { panic("schema generation called dialect") }
func (*noQueryBackend) Query(context.Context,string,...any) (db.Rows,error) { panic("schema generation queried data") }
func (*noQueryBackend) Exec(context.Context,string,...any) (db.Result,error) { panic("schema generation wrote data") }

func trace(value string) {
 file,err:=os.OpenFile(os.Getenv("OPENAPI_COMMAND_TRACE"),os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600)
 if err!=nil { panic("fixture trace unavailable") }
 _,writeErr:=io.WriteString(file,value+"\n")
 closeErr:=file.Close()
 if writeErr!=nil || closeErr!=nil { panic("fixture trace incomplete") }
}

func selectedRoutes(registry *models.Registry) (*urls.Router,error) {
 mode:=os.Getenv("OPENAPI_COMMAND_MODE")
 if mode=="opaque" {
  return urls.New(urls.Path("opaque/",http.HandlerFunc(func(http.ResponseWriter,*http.Request){panic("opaque handler invoked")}),"opaque","GET"))
 }
 title:=api.StringField("title")
 title.Source="private_title"
 if mode=="malformed" { title.Represent=func(context.Context,any)(any,error){panic("representation sampled")} }
 serializer,err:=api.New(api.Definition{Fields:[]api.Field{api.IntegerField("id"),title}})
 if err!=nil{return nil,err}
 resource,err:=api.NewResource(api.ResourceConfig{
  Store:orm.New(&noQueryBackend{},registry),Model:"catalog.Book",Serializer:serializer,AllowAnonymous:true,
  Policy:auth.PolicyFunc(func(context.Context,auth.Principal,string,auth.Resource)error{panic("authorization sampled")}),
  Scope:func(context.Context,auth.Principal,models.Schema)(db.Predicate,error){panic("scope sampled")},
 })
 if err!=nil{return nil,err}
 routes,err:=resource.ReadOpenAPIRoutes("books/","book",api.ReadOpenAPIOptions{Security:[]api.OpenAPISecurity{{Type:"public"}}})
 if err!=nil{return nil,err}
 return urls.New(urls.Include("api/","v1",routes...))
}

func main() {
 ctx:=context.Background()
 registry:=&models.Registry{}
 schema:=models.Schema{AppLabel:"catalog",Name:"Book",Fields:[]models.Field{models.BigIntegerField("id",models.Primary),models.TextField("private_title")}}
 if err:=registry.Register(schema);err!=nil{panic(err)}
 if err:=registry.Freeze();err!=nil{panic(err)}
 options:=api.OpenAPIOptions{Title:"Command Books",Version:"1.0"}
 if len(os.Args)==2 && os.Args[1]=="direct" {
  router,err:=selectedRoutes(registry)
  if err!=nil{panic(err)}
  document,err:=api.OpenAPI(ctx,router,options)
  if err!=nil{panic(err)}
  if n,err:=os.Stdout.Write(document);err!=nil || n!=len(document){panic("direct output failed")}
  return
 }
 opened,ready:=false,false
 command:=management.OpenAPICommand(func(ctx context.Context,apps *app.Registry,settings conf.Values)(*urls.Router,error){
  trace("resolve")
  value,ok:=apps.Get("metadata","models")
  if !ok || value!=registry || !opened || !ready || settings.Secret("GOGO_SCHEMA_VALUE").Reveal()!="synthetic-private-setting" {return nil,errors.New("private-resolver-cause")}
  if os.Getenv("OPENAPI_COMMAND_MODE")=="error" {return nil,errors.New("private-resolver-cause")}
  if os.Getenv("OPENAPI_COMMAND_MODE")=="panic" {panic("private-resolver-cause")}
  return selectedRoutes(registry)
 },options)
 command.Resources=[]string{"metadata"}
 command.OpenResources=true
 project:=management.Project{
  Name:"command-fixture",Root:".",Commands:[]management.Command{command},
  Schema:conf.Schema{{Name:"GOGO_SCHEMA_VALUE",Sensitive:true,RequiredFor:[]string{"metadata"}}},
  Apps:[]app.Config{{Name:"command-fixture",Label:"catalog",Register:func(r *app.Registry)error{return r.Register("metadata","models",registry)},
   Ready:func(context.Context,*app.Registry)error{if !opened{return errors.New("ready before resource")};ready=true;trace("ready");return nil},
   Shutdown:func(context.Context)error{trace("shutdown");return nil},
  }},
  ResourceFactory:func(settings conf.Values,names []string)([]app.Resource,error){
   if !reflect.DeepEqual(names,[]string{"metadata"}) || settings.Secret("GOGO_SCHEMA_VALUE").Reveal()!="synthetic-private-setting" {return nil,fmt.Errorf("unexpected selected resources")}
   trace("select:metadata")
   return []app.Resource{{Name:"metadata",Open:func(context.Context)(func(context.Context)error,error){
    opened=true;trace("open")
    return func(context.Context)error{opened=false;trace("close");return nil},nil
   }}},nil
  },
 }
 os.Exit(management.Run(ctx,project,os.Args,management.Options{}))
}
`
