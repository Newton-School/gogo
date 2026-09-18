const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');
const root = path.resolve(__dirname, '..');
const read = name => fs.readFileSync(path.join(root, name), 'utf8');
const coverage = JSON.parse(read('.generated/coverage.json'));
const sidebars = JSON.parse(read('.generated/sidebars.json'));

test('unpatched image parsers cannot be reached through Markdown images', () => {
  const transform = require('../tools/remark-image-policy.cjs')();
  for (const type of ['image', 'imageReference']) {
    assert.throws(() => transform({type: 'root', children: [{type, url: 'disguised.png'}]}, {fail(message) {throw new Error(message);}}), /images are disabled/);
  }
  transform({type: 'root', children: [{type: 'code', value: '![example](image.png)'}]}, {fail() {assert.fail('Code is not an image');}});
});

test('every generated guide, note, and API document is built', () => {
  const sources = fs.readdirSync(path.join(root, '.generated/content')).filter(name => name.endsWith('.md'));
  assert.equal(sources.length, coverage.pages);
  assert.equal(coverage.pages, coverage.featureGuides + coverage.technicalGuides + coverage.publicPackages + 3);
  for (const source of sources) {
    const html = read(`build/docs/${source.slice(0, -3)}/index.html`);
    assert.match(html, /<main/);
    assert.doesNotMatch(html, /\{\{(?:code|include) /);
    assert.match(html, /theme-doc-breadcrumbs/);
  }
});

test('navigation separates the first-project journey from feature guides and reference', () => {
  assert.deepEqual(Object.keys(sidebars), ['startSidebar', 'guideSidebar', 'adminSidebar', 'asyncSidebar', 'referenceSidebar']);
  for (const [name, items] of Object.entries(sidebars)) {
    assert.ok(items.length <= 15, `${name} has too many root entries`);
    function walk(nodes, depth) {
      assert.ok(depth <= 3, `${name} is too deeply nested`);
      for (const node of nodes) {
        if (node.type === 'category') {
          assert.equal(node.collapsed, true);
          assert.ok(node.link, `${node.label} needs a landing page`);
          assert.ok(node.description?.length > 10, `${node.label} needs a useful overview-card description`);
          walk(node.items, depth + 1);
        }
        if (name !== 'referenceSidebar' && node.type === 'doc') assert.ok(!node.id.startsWith('api-'));
      }
    }
    walk(items, 1);
  }
});

test('home page offers distinct tutorial, feature, and lookup entry points', () => {
  const html = read('build/index.html');
  for (const destination of ['quickstart', 'showcase', 'installation', 'features', 'admin-wiring', 'async-wiring', 'running', 'docker', 'settings', 'packages']) {
    assert.match(html, new RegExp(`href="/docs/${destination}/"`));
  }
  assert.match(html, /1.0.0-alpha.1/);
  assert.match(html, /not complete Django or Celery parity/);
});

test('search indexes are generated locally and include features and symbols', () => {
  const files = fs.readdirSync(path.join(root, 'build')).filter(name => /^search-index.*\.json$/.test(name));
  assert.ok(files.length > 0, 'Production search index missing');
  const index = files.map(name => read('build/' + name)).join('\n');
  assert.match(index, /ForeignKeyField/);
  assert.match(index, /[Cc]hord/);
  assert.match(index, /GOGO_DATABASE_URL/);
});

test('reference type anchors remain stable without enormous symbol outlines', () => {
  const html = read('build/docs/api-core-models/index.html');
  assert.match(html, /id="field"/);
  assert.match(html, /id="schema"/);
  assert.match(read('.generated/content/api-core-models.md'), /toc_max_heading_level: 2/);
  assert.match(read('.generated/content/api-admin.md'), /\{#modeladmin\}/);
});

test('every extracted declaration is represented once, including type methods', () => {
  const count = fs.readdirSync(path.join(root, '.generated/content'))
    .filter(name => name.startsWith('api-'))
    .reduce((total, name) => total + (read('.generated/content/' + name).match(/^\[Source\]/gm) || []).length, 0);
  assert.equal(count, coverage.declarations);
});

test('public output contains no source include directives or machine paths', () => {
  for (const name of fs.readdirSync(path.join(root, '.generated/content'))) {
    const source = read('.generated/content/' + name);
    assert.doesNotMatch(source, /\/Users\/|\/home\/[A-Za-z]|file:\/\//);
  }
});
