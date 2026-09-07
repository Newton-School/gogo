"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");
const script = fs.readFileSync(path.join(__dirname, "../internal/assets/admin.js"), "utf8");

function fixture(options = {}) {
  const form = {};
  function control(id, value = "") {
    const handlers = new Map();
    return { id, value, form, tagName: "INPUT", disabled: false, readOnly: false,
      addEventListener(type, callback) { const events = handlers.get(type) || []; events.push(callback); handlers.set(type, events); },
      fire(type) { for (const callback of handlers.get(type) || []) callback(); }
    };
  }
  const title = control("id_title"), subtitle = control("id_subtitle");
  subtitle.tagName = "TEXTAREA";
  const target = control("id_slug", options.initial || "");
  target.dataset = { prepopulateFrom: JSON.stringify([title.id, subtitle.id]), prepopulateMaxlength: String(options.maxlength ?? 40), prepopulateUnicode: String(options.unicode ?? false) };
  const controls = new Map([[title.id, title], [subtitle.id, subtitle], [target.id, target]]);
  options.modify?.({ target, title, subtitle, controls });
  const document = { querySelectorAll(selector) { return selector === "input[data-prepopulate-from]" ? [target] : []; }, getElementById(id) { return controls.get(id); } };
  vm.runInNewContext(script, { document }, { timeout: 1000 });
  return { target, title, subtitle };
}

test("ordered sources produce an ASCII slug within its declared limit", () => {
  const {target,title,subtitle} = fixture({maxlength:20});
  title.value = "  Café & Crème "; subtitle.value = "Today's Choices"; title.fire("input");
  assert.equal(target.value,"cafe-creme-todays-ch");
  subtitle.value = "New"; subtitle.fire("change");
  assert.equal(target.value,"cafe-creme-new");
});
test("pre-existing or manually edited target is never overwritten", () => {
  for (const initial of ["saved-slug", " "]) {
    const {target,title} = fixture({initial}); title.value = "New title"; title.fire("input"); assert.equal(target.value,initial);
  }
  for (const event of ["input","change"]) {
    const {target,title} = fixture(); title.value = "Initial"; title.fire("input");
    target.value = ""; target.fire(event); title.value = "Changed"; title.fire("input"); assert.equal(target.value,"");
  }
});
test("missing cross-form disabled and malformed configurations fail closed", () => {
  const variants = [
    ({controls}) => controls.delete("id_title"),
    ({title}) => {title.form = {};},
    ({title}) => {title.disabled = true;},
    ({target}) => {target.readOnly = true;},
    ({target}) => {target.dataset.prepopulateFrom = "{invalid";},
    ({target}) => {target.dataset.prepopulateFrom = "[]";},
    ({target}) => {target.dataset.prepopulateMaxlength = "NaN";},
    ({target}) => {target.dataset.prepopulateUnicode = "yes";},
    ({subtitle}) => {subtitle.tagName = "SCRIPT";}
  ];
  for (const modify of variants) { const {target,title} = fixture({modify}); title.value="Changed"; title.fire("input"); assert.equal(target.value,""); }
});
test("runtime read-only changes stop suggestions without touching inputs", () => {
  const {target,title} = fixture(); title.value="Before"; title.fire("input"); title.readOnly=true; title.value="After"; title.fire("input");
  assert.equal(target.value,"before"); assert.equal(title.value,"After");
});
test("markup and long source values remain data and bounded ASCII", () => {
  const {target,title} = fixture({maxlength:0}); title.value="<script>alert(1)</script>"; title.fire("input");
  assert.equal(target.value,"scriptalert1script");
  title.value = "a".repeat(100000); title.fire("input"); assert.equal(target.value.length,65536);
  title.value = "東京 😀"; title.fire("input"); assert.equal(target.value,"");
});
test("Unicode mode retains normalized letters and truncates by code point", () => {
  const {target,title} = fixture({unicode:true,maxlength:40});
  title.value="  東京 Café 😀 ＧＯＧＯ  "; title.fire("input");
  assert.equal(target.value,"東京-café-gogo");
  const short = fixture({unicode:true,maxlength:2});
  short.title.value="𐐀𐐁𐐂"; short.title.fire("input");
  assert.equal(short.target.value,"𐐨𐐩");
  assert.equal(Array.from(short.target.value).length,2);
});
