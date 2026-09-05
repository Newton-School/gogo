package conf

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRedactEveryFormat(t *testing.T) {
	s := NewSecret("private-password")
	for _, v := range []string{fmt.Sprint(s), fmt.Sprintf("%+v %#v %q", s, s, s), func() string { b, _ := json.Marshal(s); return string(b) }()} {
		if strings.Contains(v, "private-password") {
			t.Fatal("secret leaked")
		}
	}
}
func TestRequirementsAndParsing(t *testing.T) {
	schema := CoreSchema()
	if _, e := schema.Load(nil, "database"); e == nil {
		t.Fatal("missing database accepted")
	}
	if _, e := schema.Load(map[string]string{"GOGO_DEBUG": "secret-invalid"}); e == nil || strings.Contains(e.Error(), "secret-invalid") {
		t.Fatal(e)
	}
	v, e := schema.Load(nil)
	if e != nil || v.Bool("GOGO_DEBUG") {
		t.Fatal(e)
	}
	list := v.List("GOGO_ALLOWED_HOSTS")
	list[0] = "evil"
	if v.List("GOGO_ALLOWED_HOSTS")[0] == "evil" {
		t.Fatal("mutable settings")
	}
}
func TestEnvLiteralAndDuplicate(t *testing.T) {
	v, e := ReadEnv(strings.NewReader("# group\nGOGO_SECRET_KEY='$(do-not-execute)'\n"))
	if e != nil || v["GOGO_SECRET_KEY"] != "$(do-not-execute)" {
		t.Fatal(e)
	}
	if _, e = ReadEnv(strings.NewReader("GOGO_ENV=test\nGOGO_ENV=production")); e == nil {
		t.Fatal("duplicate accepted")
	}
}
