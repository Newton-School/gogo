package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth/views"
)

// This executes the shipped JavaScript against a minimal DOM fixture, not a
// browser. It proves fragment handling independently of Go string assertions;
// real-browser accessibility, CSP and visual checks remain separate gates.
func TestPasswordResetScriptFragmentToForm(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; reset script execution not verified")
	}
	w := httptest.NewRecorder()
	views.PasswordResetConfirmScript().ServeHTTP(w, httptest.NewRequest("GET", views.PasswordResetConfirmScriptPath(), nil))
	if w.Code != 200 {
		t.Fatal("reset script unavailable")
	}
	const token = "12345678-1234-1234-1234-123456789abc.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	payload, err := json.Marshal(map[string]string{"script": w.Body.String(), "token": token})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", `
const vm = require('node:vm');
const fs = require('node:fs');
const {script, token} = JSON.parse(fs.readFileSync(0, 'utf8'));
const tests = [
  {hash: '', value: '', expected: '', hidden: false},
  {hash: '#'+token, value: '', expected: token, hidden: true},
  {hash: '#'+encodeURIComponent(token), value: '', expected: token, hidden: true},
  {hash: '#<script>invalid</script>', value: token, expected: '', hidden: false},
  {hash: '#%E0%A4%A', value: '', expected: '', hidden: false},
  {hash: '', value: token, expected: token, hidden: true},
  {hash: '', value: 'invalid token', expected: '', hidden: false},
  {hash: '#'+token, value: '', expected: token, noRow: true},
  {hash: '#'+token, noInput: true}
];
for (const [index, test] of tests.entries()) {
  const input = {value: test.value};
  const row = {hidden: false};
  const replacements = [];
  const context = {
    document: {title: 'Reset', getElementById(id) {
      if (id === 'id_reset_token') return test.noInput ? null : input;
      if (id === 'reset-token-row') return test.noRow ? null : row;
      throw new Error('Unexpected DOM access');
    }},
    window: {
      location: {hash: test.hash, pathname: '/accounts/reset/', search: '?locale=en'},
      history: {replaceState(state, title, url) { replacements.push({state, title, url}); }}
    }
  };
  vm.runInNewContext(script, context, {timeout: 1000});
  const fail = () => { throw new Error('Fragment transfer case '+index+' failed'); };
  if (test.noInput) { if (replacements.length !== 0) fail(); continue; }
  if (input.value !== test.expected || (!test.noRow && row.hidden !== test.hidden)) fail();
  if (replacements.length !== (test.hash ? 1 : 0)) fail();
  if (test.hash && (replacements[0].state !== null || replacements[0].url !== '/accounts/reset/?locale=en')) fail();
}
`)
	command.Stdin = bytes.NewReader(payload)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reset script fixture failed: %v\n%s", err, output)
	}
}
