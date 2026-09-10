"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { searchDocumentation } = require("../assets/site.js");

const entries = [
  { title: "Model fields", group: "Data", text: "ForeignKeyField relates models.", url: "model-fields.html" },
  { title: "ForeignKeyField", group: "core/models", text: "ForeignKeyField", url: "api-core-models.html#foreignkeyfield" },
  { title: "Admin", group: "Admin", text: "Configure readonly fields and model actions.", url: "admin.html" },
];

test("exact symbols rank before guide-body mentions", () => {
  const results = searchDocumentation(entries, "ForeignKeyField");
  assert.equal(results[0].item.url, "api-core-models.html#foreignkeyfield");
  assert.equal(results[1].item.url, "model-fields.html");
});
test("search is case insensitive and requires every query word", () => {
  assert.equal(searchDocumentation(entries, "  READONLY model  ")[0].item.title, "Admin");
  assert.equal(searchDocumentation(entries, "readonly missing").length, 0);
});
test("empty and absent matches produce no results", () => {
  assert.deepEqual(searchDocumentation(entries, "   "), []);
  assert.deepEqual(searchDocumentation(entries, "unmatched-feature"), []);
});
test("search bounds result count without changing the source index", () => {
  const many = Array.from({ length: 100 }, (_, i) => ({ title: `Task ${i}`, group: "Async", text: "worker", url: `task-${i}.html` }));
  const before = JSON.stringify(many);
  assert.equal(searchDocumentation(many, "task").length, 40);
  assert.equal(JSON.stringify(many), before);
});
test("search does not interpret a query as markup or a regular expression", () => {
  assert.equal(searchDocumentation(entries, "<script>").length, 0);
  assert.equal(searchDocumentation(entries, ".*").length, 0);
});
