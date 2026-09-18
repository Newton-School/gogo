const fs = require('node:fs/promises');
const path = require('node:path');

// Old document URLs remain entry points, not extra articles or search results.
module.exports = function consolidatedPages(context) {
  return {
    name: 'gogo-consolidated-pages',
    getClientModules() {
      return [path.join(context.siteDir, 'src', 'feature-links.js')];
    },
    async postBuild({outDir, baseUrl}) {
      const routes = JSON.parse(await fs.readFile(path.join(context.siteDir, '.generated', 'routes.json'), 'utf8'));
      for (const [source, route] of Object.entries(routes)) {
        if (source === route.page) continue;
        if (![source, route.page, route.anchor].every(value => /^[a-z0-9-]+$/.test(value))) throw new Error('Unsafe documentation redirect');
        const target = `${baseUrl}docs/${route.page}/`;
        const data = JSON.stringify({target, anchor: route.anchor, anchors: route.anchors}).replace(/</g, '\\u003c');
        const output = path.join(outDir, 'docs', source);
        await fs.mkdir(output, {recursive: true});
        await fs.writeFile(path.join(output, 'index.html'), `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="robots" content="noindex"><title>Documentation moved</title><link rel="canonical" href="${target}"></head><body><p>This section is now part of <a href="${target}#${route.anchor}">the feature reference</a>.</p><script>const route=${data};let hash='';try{hash=decodeURIComponent(location.hash.slice(1))}catch{}location.replace(route.target+'#'+(Object.hasOwn(route.anchors,hash)?route.anchors[hash]:route.anchor));</script></body></html>`);
      }
    },
  };
};
