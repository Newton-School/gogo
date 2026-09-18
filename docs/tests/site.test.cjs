const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');
const vm = require('node:vm');
const root = path.resolve(__dirname, '..');
const read = name => fs.readFileSync(path.join(root, name), 'utf8');
const coverage = JSON.parse(read('.generated/coverage.json'));
const sidebars = JSON.parse(read('.generated/sidebars.json'));
const routes = JSON.parse(read('.generated/routes.json'));

test('the image guard still rejects Markdown images after dependency upgrades', () => {
  const transform = require('../tools/remark-image-policy.cjs')();
  for (const type of ['image', 'imageReference']) {
    assert.throws(() => transform({type: 'root', children: [{type, url: 'disguised.png'}]}, {fail(message) {throw new Error(message);}}), /images are disabled/);
  }
  transform({type: 'root', children: [{type: 'code', value: '![example](image.png)'}]}, {fail() {assert.fail('Code is not an image');}});
});

test('every source has a focused page instead of a hidden section', () => {
  const sources = fs.readdirSync(path.join(root, '.generated/content')).filter(name => name.endsWith('.md'));
  assert.equal(sources.length, coverage.pages);
  assert.equal(coverage.pages, coverage.sourceDocuments);
  assert.equal(coverage.sourceDocuments, coverage.featureGuides + coverage.technicalGuides + coverage.publicPackages + 3);
  assert.equal(Object.keys(routes).length, coverage.sourceDocuments);
  for (const source of sources) {
    const html = read(`build/docs/${source.slice(0, -3)}/index.html`);
    assert.match(html, /<main/);
    assert.doesNotMatch(html, /\{\{(?:code|include|snippet) /);
    assert.doesNotMatch(html, /docs:(?:begin|end) /);
    assert.match(html, /theme-doc-breadcrumbs/);
  }
});

test('only Docs Admin and Async exist with direct technical page names', () => {
  assert.deepEqual(Object.keys(sidebars), ['docsSidebar', 'adminSidebar', 'asyncSidebar']);
  const config = require('../docusaurus.config.js');
  assert.deepEqual(config.themeConfig.navbar.items.map(item => item.label), ['Docs', 'Admin', 'Async']);
  for (const [name, items] of Object.entries(sidebars)) {
    assert.ok(items.length <= 15, `${name} has too many root entries`);
    function walk(nodes, depth) {
      assert.ok(depth <= 4, `${name} is too deeply nested`);
      for (const node of nodes) {
        if (node.type === 'category') {
          assert.equal(node.link, undefined, `${node.label} must expand without opening a page`);
          assert.ok(node.items.length > 0, `${node.label} is an empty category`);
          walk(node.items, depth + 1);
        }
        assert.ok(node.label.split(/\s+/).length <= 4, `Use a direct feature name: ${node.label}`);
      }
    }
    walk(items, 1);
  }
});

test('all documents are leaf nodes exactly once in the correct feature hierarchy', () => {
  const locations = new Map();
  function walk(items, parents) {
    for (const item of items) {
      if (item.type === 'category') walk(item.items, [...parents, item.label]);
      else {
        assert.equal(item.type, 'doc');
        assert.equal(item.items, undefined);
        assert.ok(!locations.has(item.id), `Duplicate navigation entry: ${item.id}`);
        locations.set(item.id, parents);
      }
    }
  }
  for (const [name, items] of Object.entries(sidebars)) walk(items, [name]);
  assert.equal(locations.size, coverage.pages);
  for (const page of Object.keys(routes)) assert.ok(locations.has(page), `Missing leaf: ${page}`);
  const expected = {
    index: ['docsSidebar'],
    settings: ['docsSidebar', 'Project', 'Configuration'],
    'detail-core-api-idempotency': ['docsSidebar', 'API', 'Mutations'],
    'detail-core-api-openapi': ['docsSidebar', 'API'],
    'detail-core-orm-routing': ['docsSidebar', 'Database', 'Transactions'],
    'template-vocabulary': ['docsSidebar', 'Presentation', 'Templates'],
    'options-connectors-postgres-config': ['docsSidebar', 'Operations', 'Connectors', 'PostgreSQL'],
    'options-connectors-redis-config': ['docsSidebar', 'Operations', 'Connectors', 'Redis'],
    'detail-async-result': ['asyncSidebar', 'Results'],
    'options-async-retrypolicy': ['asyncSidebar', 'Retries'],
    'api-async-testing': ['asyncSidebar', 'Testing'],
    'options-async-worker': ['asyncSidebar', 'Workers'],
  };
  for (const [id, parents] of Object.entries(expected)) assert.deepEqual(locations.get(id), parents, id);
  assert.ok(!fs.existsSync(path.join(root, 'build/docs/category')), 'No category landing pages should be generated');
});

test('the home URL opens technical documentation instead of a marketing page', () => {
  assert.match(read('build/index.html'), /\/docs\/index\//);
  const html = read('build/docs/index/index.html');
  assert.match(html, /theme-doc-sidebar/);
  assert.match(html, /\/docs\/quickstart\//);
  assert.match(html, /1.0.0-alpha.1/);
  assert.match(html, /not a stable 1.0/);
  assert.doesNotMatch(html, /From your first route/);
});

test('search indexes are generated locally and include features and symbols', () => {
  const files = fs.readdirSync(path.join(root, 'build')).filter(name => /^search-index.*\.json$/.test(name));
  assert.ok(files.length > 0, 'Production search index missing');
  const index = files.map(name => read('build/' + name)).join('\n');
  assert.match(index, /ForeignKeyField/);
  assert.match(index, /[Cc]hord/);
  assert.match(index, /GOGO_DATABASE_URL/);
});

test('signatures and detailed contracts have their own discoverable pages', () => {
  const html = read('build/docs/api-core-models/index.html');
  assert.match(html, /id="api-core-models-field"/);
  assert.match(html, /id="api-core-models-schema"/);
  assert.match(read('.generated/content/api-admin.md'), /\{#api-admin-modeladmin\}/);
  assert.equal(routes['detail-async-group-result'].page, 'detail-async-group-result');
  assert.equal(routes['detail-async-testing-periodic'].page, 'detail-async-testing-periodic');
});

test('every configuration setting has a description in the built reference', () => {
  const descriptions = JSON.parse(read('settings-descriptions.json'));
  const html = read('build/docs/settings/index.html');
  const entries = [...html.matchAll(/<section class="settings-entry">([\s\S]*?)<\/section>/g)].map(match => match[1]);
  for (const [name, description] of Object.entries(descriptions)) {
    const escaped = description.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#x27;');
    assert.ok(entries.some(entry => entry.includes(name) && entry.includes(escaped) && entry.includes('language-dotenv')), `${name} has no code example and adjacent description`);
  }
});

test('every extracted declaration is represented once, including type methods', () => {
  const count = fs.readdirSync(path.join(root, '.generated/content'))
    .filter(name => name.startsWith('api-') && name !== 'api-writes.md' && name.endsWith('.md'))
    .reduce((total, name) => total + (read('.generated/content/' + name).match(/^#{3,4} .+ \{#api-/gm) || []).length, 0);
  assert.equal(count, coverage.declarations);
});

test('field kinds and reviewed configuration members have focused references', () => {
  const data = JSON.parse(read('fields.json'));
  let fieldPages = 0;
  for (const [family, rows] of Object.entries(data)) {
    for (const row of rows) {
      const id = `field-${family}-${row[0].toLowerCase()}`;
      assert.equal(routes[id].page, id);
      const html = read(`build/docs/${id}/index.html`);
      assert.match(html, /Configuration/);
      assert.match(html, /package/);
      fieldPages++;
    }
  }
  assert.equal(fieldPages, coverage.fieldPages);
  let optionPages = 0;
  for (const file of fs.readdirSync(root).filter(name => /^options.*\.json$/.test(name))) {
    for (const [name, entry] of Object.entries(JSON.parse(read(file)))) {
      const id = 'options-' + name.toLowerCase().replace(/[^a-z0-9]+/g, '-');
      const html = read(`build/docs/${id}/index.html`);
      for (const member of Object.keys(entry.fields)) {
        assert.ok(html.includes(`id="${id}-${member.toLowerCase()}"`), `${name}.${member} lost its explanation`);
      }
      optionPages++;
    }
  }
  assert.equal(optionPages, coverage.optionPages);
});

test('small field examples come before expandable complete programs', () => {
  const data = JSON.parse(read('fields.json'));
  for (const [family, rows] of Object.entries(data)) {
    for (const [kind] of rows) {
      const source = read(`.generated/content/field-${family}-${kind.toLowerCase()}.md`);
      assert.ok(source.indexOf('**1. Declare the field**') < source.indexOf('<details>'));
      const complete = source.slice(source.indexOf('<details>'), source.indexOf('</details>'));
      assert.match(complete, /Complete runnable example, including imports/);
      assert.match(complete, /package main/);
      const visible = source.replace(/<details[^>]*>[\s\S]*?<\/details>/g, '');
      for (const match of visible.matchAll(/```go\n([\s\S]*?)\n```/g)) {
        assert.ok(match[1].split('\n').length <= 20, `${family}.${kind} has an oversized visible example`);
      }
    }
  }
});

test('curated API examples render before declarations without losing anchors', () => {
  const entries = JSON.parse(read('code-examples.json'));
  for (const entry of Object.values(entries)) {
    for (const key of entry.symbols) {
      const dot = key.indexOf('.');
      const page = 'api-' + key.slice(0, dot).replaceAll('/', '-');
      const anchor = key.slice(dot + 1).toLowerCase().replaceAll('.', '-');
      const source = read(`.generated/content/${page}.md`);
      const start = source.indexOf(`{#${page}-${anchor}}`);
      assert.ok(start >= 0, `Missing ${key}`);
      const symbol = source.slice(start, source.indexOf('</section>', start));
      assert.ok(symbol.indexOf('**Example**') >= 0, `Missing example for ${key}`);
      assert.ok(symbol.indexOf('**Example**') < symbol.indexOf('Go declaration'), key);
    }
  }
});

test('old document URLs redirect to real consolidated anchors', () => {
  const idSets = new Map();
  for (const [source, route] of Object.entries(routes)) {
    if (!idSets.has(route.page)) {
      const html = read(`build/docs/${route.page}/index.html`);
      const ids = [...html.matchAll(/\sid="([^"]+)"/g)].map(match => match[1]);
      assert.equal(new Set(ids).size, ids.length, `${route.page} has duplicate element IDs`);
      idSets.set(route.page, new Set(ids));
    }
    for (const anchor of Object.values(route.anchors)) {
      assert.ok(idSets.get(route.page).has(anchor), `${source} lost #${anchor}`);
    }
    if (source !== route.page) assert.match(read(`build/docs/${source}/index.html`), /name="robots" content="noindex"/);
  }
  assert.deepEqual(routes.models.legacyAnchors['api-core-models-field'], {page: 'api-core-models', anchor: 'api-core-models-field'});
  for (const route of Object.values(routes)) {
    for (const target of Object.values(route.legacyAnchors || {})) {
      assert.ok(idSets.get(target.page).has(target.anchor), `Lost legacy target ${target.page}#${target.anchor}`);
    }
  }
});

test('hash navigation expands the referenced contract or declaration', () => {
  assert.match(read('src/theme/Details/index.js'), /<details \{\.\.\.props\}>/);
  assert.doesNotMatch(read('src/theme/Details/index.js'), /Collapsible|useState/);
  const source = read('src/feature-links.js').replace(/^import .+;\n/m, '').replace('export function', 'function');
  const details = {tagName: 'DETAILS', open: false, parentElement: null};
  let scrolled = false;
  const target = {parentElement: details, scrollIntoView() { scrolled = true; }};
  const context = {routes, requestAnimationFrame(callback) { callback(); }, document: {getElementById(id) { return id === 'models-definition' ? target : null; }}};
  vm.createContext(context);
  vm.runInContext(source + '\nonRouteDidUpdate({location: {pathname: "/docs/models/", hash: "#models-definition"}});', context);
  assert.equal(details.open, true);
  assert.equal(scrolled, true);
});

test('old feature anchors navigate to focused pages', () => {
  const source = read('src/feature-links.js').replace(/^import .+;\n/m, '').replace('export function', 'function');
  let destination;
  const context = {routes, requestAnimationFrame(callback) {callback();}, document: {getElementById() {return null;}}, window: {location: {replace(value) {destination = value;}}}};
  vm.runInNewContext(source + '\nonRouteDidUpdate({location: {pathname: "/docs/models/", hash: "#api-core-models-field"}});', context);
  assert.equal(destination, '/docs/api-core-models/#api-core-models-field');
});

test('public output contains no source include directives or machine paths', () => {
  for (const name of fs.readdirSync(path.join(root, '.generated/content'))) {
    const source = read('.generated/content/' + name);
    assert.doesNotMatch(source, /\/Users\/|\/home\/[A-Za-z]|file:\/\//);
  }
});

test('documentation keeps examples and API signatures without GitHub source links', () => {
  for (const id of Object.keys(routes)) {
    const html = read(`build/docs/${id}/index.html`);
    assert.doesNotMatch(html, /href="https?:\/\/(?:www\.)?github\.com\/[^"\s]+\/(?:blob|tree)\//, id);
    assert.doesNotMatch(html, /href="https?:\/\/raw\.githubusercontent\.com\//, id);
    assert.doesNotMatch(html, />Example source</, id);
    assert.doesNotMatch(read(`.generated/content/${id}.md`), /^\[(?:Source|Example source)\]/m, id);
  }
  assert.match(read('.generated/content/api-core-models.md'), /type Field struct/);
  assert.match(read('.generated/content/field-models-slug.md'), /package main/);
});

test('API references use readable symbol sections and structured call contracts', () => {
  for (const name of fs.readdirSync(path.join(root, '.generated/content')).filter(name => name.startsWith('api-') && name !== 'api-writes.md')) {
    const source = read('.generated/content/' + name);
    const html = read(`build/docs/${name.slice(0, -3)}/index.html`);
    assert.doesNotMatch(source, /embedded or positional|\*\*Input:\*\*|\*\*Output:\*\*/);
    assert.match(html, /class="reference-entry"/);
    assert.doesNotMatch(html, /className=/);
    if (source.includes('className="reference-call"')) assert.match(html, /class="reference-io"/);
  }
  const html = read('build/docs/api-core-models/index.html');
  assert.match(html, /class="reference-index"/);
  assert.match(html, /class="reference-method"/);
  assert.match(html, /<details class="reference-declaration">/);
  assert.match(html, /<summary>Go declaration<\/summary>/);
  assert.match(html, /id="api-core-models-field"/);
});
