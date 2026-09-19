"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");
const script = fs.readFileSync(path.join(__dirname, "../internal/assets/admin.js"), "utf8");

function datetimeFixture({value = "2026-01-02T03:04:05.123", disabled = false, readOnly = false} = {}) {
  const elements = [];
  function element(tagName) {
    const handlers = new Map(), attributes = new Map();
    const el = {tagName, children: [], value: "", addEventListener(name, fn) { handlers.set(name, fn); },
      fire(name) { handlers.get(name)?.(); }, append(...children) { this.children.push(...children); },
      hasAttribute(name) { return attributes.has(name); }, getAttribute(name) { return attributes.get(name); },
      setAttribute(name, value) { attributes.set(name, value); }
    };
    elements.push(el);
    return el;
  }
  const label = {htmlFor: "id_created"};
  const form = element("FORM");
  form.querySelectorAll = () => [label];
  const original = element("INPUT");
  Object.assign(original, {id: "id_created", name: "created", type: "datetime-local", value, disabled, readOnly, required: true, form,
    after(node) { this.enhancement = node; }
  });
  original.setAttribute("aria-describedby", "id_created_help id_created_errors");
  original.setAttribute("aria-invalid", "true");
  const document = {createElement: element, querySelectorAll(selector) { return selector === "input.vDateTimeField" ? [original] : []; }};
  const window = {addEventListener() {}};
  vm.runInNewContext(script, {document, window}, {timeout: 1000});
  return {original, form, label, parts: elements.filter(el => el !== original && el.tagName === "input"), window};
}

test("Django datetime controls keep Gogo's POST name and fractional seconds", () => {
  const {original, form, parts, label} = datetimeFixture();
  assert.equal(original.name, "created");
  assert.equal(original.type, "hidden");
  assert.deepEqual(parts.map(part => part.id), ["id_created_0", "id_created_1"]);
  assert.equal(label.htmlFor, parts[0].id);
  assert.equal(parts[0].name, undefined);
  assert.equal(parts[1].name, undefined);
  assert.equal(parts[1].value, "03:04:05.123");
  for (const part of parts) assert.equal(part.getAttribute("aria-describedby"), "id_created_help id_created_errors");
  // Upstream calendar shortcuts set value without emitting input/change.
  parts[0].value = "2026-01-03";
  form.fire("submit");
  assert.equal(original.value, "2026-01-03T03:04:05.123");
});

test("blank and incomplete datetimes reach server validation without invented values", () => {
  const {original, form, parts} = datetimeFixture({value: ""});
  form.fire("submit");
  assert.equal(original.value, "");
  parts[0].value = "2026-02-30";
  form.fire("submit");
  assert.equal(original.value, "2026-02-30T");
  parts[1].value = "not a time";
  form.fire("submit");
  assert.equal(original.value, "2026-02-30Tnot a time");
});

test("disabled and readonly datetime controls are never enhanced or made editable", () => {
  for (const config of [{disabled: true}, {readOnly: true}]) {
    const {original, parts} = datetimeFixture(config);
    assert.equal(parts.length, 0);
    assert.equal(original.type, "datetime-local");
    assert.equal(original.enhancement, undefined);
  }
});

test("Django English format bridge preserves named interpolation and ISO formats", () => {
  const {window} = datetimeFixture();
  assert.equal(window.interpolate("%(sel)s of %(cnt)s selected", {sel: 1, cnt: 3}, true), "1 of 3 selected");
  assert.equal(window.ngettext("one", "many", 1), "one");
  assert.equal(window.ngettext("one", "many", 2), "many");
  assert.equal(window.get_format("DATE_INPUT_FORMATS")[0], "%Y-%m-%d");
  assert.equal(window.get_format("TIME_INPUT_FORMATS")[0], "%H:%M:%S");
});

function relationFixture({endpoint = "/admin/shop/editor/lookup/", disabled = false, readOnly = false, response} = {}) {
  const label = {htmlFor: "id_parent"};
  const source = {id: "id_parent", name: "parent", type: "text", value: "1", required: true, disabled, readOnly,
    dataset: {relationUrl: endpoint}, form: {querySelectorAll: () => [label]},
    hasAttribute: () => false, getAttribute: () => null, after(select) { this.select = select; }};
  let load, settings, change;
  const calls = [];
  const jquery = () => ({on(_event, fn) { change = fn; }, select2(value) { settings = value; }});
  jquery.fn = {select2: true};
  const window = {django: {jQuery: jquery}, location: {href: "https://admin.example.test/admin/", origin: "https://admin.example.test"},
    addEventListener(_event, fn) { load = fn; }};
  const document = {querySelectorAll: selector => selector === "input[data-relation-url]" ? [source] : [], getElementById: () => null,
    createElement: () => ({children: [], append(option) { this.children.push(option); }, classList: {add() {}}, setAttribute() {}})};
  function Option(text, value, _default, selected) { Object.assign(this, {text, value, selected}); }
  const fetch = async (url, options) => {
    calls.push({url, options});
    return {ok: true, text: async () => response ?? JSON.stringify({results: [{id: "2", text: "<b>Untrusted label</b>", children: [{id: "injected"}]}], pagination: {more: true}})};
  };
  vm.runInNewContext(script, {document, window, URL, Option, fetch, AbortController}, {timeout: 1000});
  load();
  return {source, label, calls, settings, select(value) { source.select.value = value; change(); }};
}

test("Django autocomplete preserves POST identity and keeps response labels as data", async () => {
  const fixture = relationFixture();
  assert.equal(fixture.source.type, "hidden");
  assert.equal(fixture.source.name, "parent");
  assert.equal(fixture.source.select.name, undefined);
  assert.equal(fixture.label.htmlFor, "id_parent_select");
  fixture.select("2");
  assert.equal(fixture.source.value, "2");
  const result = await new Promise((resolve, reject) => fixture.settings.ajax.transport({data: {term: "public", page: 2}}, resolve, reject));
  assert.deepEqual(JSON.parse(JSON.stringify(result)), {results: [{id: "2", text: "<b>Untrusted label</b>"}], pagination: {more: true}});
  assert.equal(fixture.calls[0].url.origin, "https://admin.example.test");
  assert.equal(fixture.calls[0].url.searchParams.get("page"), "2");
  assert.equal(fixture.calls[0].options.credentials, "same-origin");
  assert.equal(fixture.calls[0].options.cache, "no-store");
});

test("autocomplete rejects unsafe configuration and bounded request failures", async () => {
  for (const config of [{endpoint: "https://untrusted.example/lookup"}, {endpoint: "http://["}, {disabled: true}, {readOnly: true}]) {
    const fixture = relationFixture(config);
    assert.equal(fixture.settings, undefined);
    assert.equal(fixture.source.type, "text");
    assert.equal(fixture.calls.length, 0);
  }
  for (const data of [{term: "x".repeat(257)}, {page: 101}, {page: 0.5}, {page: -1}]) {
    const fixture = relationFixture();
    let failed = false;
    fixture.settings.ajax.transport({data}, () => assert.fail("must not succeed"), () => { failed = true; });
    assert.equal(failed, true);
    assert.equal(fixture.calls.length, 0);
  }
  for (const response of ["not json", "x".repeat(1048577), JSON.stringify({results: [{id: 2, text: "Wrong type"}]}), JSON.stringify({results: Array(21).fill({id: "1", text: "Oversized"})})]) {
    const fixture = relationFixture({response});
    await new Promise(resolve => fixture.settings.ajax.transport({data: {}}, () => assert.fail("invalid response accepted"), resolve));
  }
});

test("aborted autocomplete requests cannot publish stale results", async () => {
  const fixture = relationFixture();
  let published = false;
  const request = fixture.settings.ajax.transport({data: {}}, () => { published = true; }, () => { published = true; });
  request.abort();
  await new Promise(setImmediate);
  assert.equal(fixture.calls[0].options.signal.aborted, true);
  assert.equal(published, false);
});
