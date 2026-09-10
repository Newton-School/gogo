const {themes} = require('prism-react-renderer');

/** @type {import('@docusaurus/types').Config} */
module.exports = {
  title: 'Gogo',
  tagline: 'A structured Go backend, from your first app to background workers.',
  // Local preview is the default. Set these two values when choosing a public host.
  url: 'http://localhost:3000',
  baseUrl: '/',
  trailingSlash: true,
  favicon: 'img/gogo.svg',
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  onDuplicateRoutes: 'throw',
  markdown: {
    format: 'detect',
    hooks: {onBrokenMarkdownLinks: 'throw', onBrokenMarkdownImages: 'throw'},
  },
  presets: [['classic', {
    docs: {
      path: '.generated/content',
      sidebarPath: require.resolve('./sidebars.js'),
      showLastUpdateAuthor: false,
      showLastUpdateTime: false,
      beforeDefaultRemarkPlugins: [require('./tools/remark-image-policy.cjs')],
    },
    pages: {beforeDefaultRemarkPlugins: [require('./tools/remark-image-policy.cjs')]},
    blog: false,
    theme: {customCss: require.resolve('./src/css/custom.css')},
    sitemap: false,
  }]],
  themes: [['@easyops-cn/docusaurus-search-local', {
    hashed: true,
    language: ['en'],
    indexBlog: false,
    indexPages: true,
    docsDir: '.generated/content',
    docsRouteBasePath: '/docs',
    highlightSearchTermsOnTargetPage: true,
    searchResultLimits: 8,
    searchResultContextMaxLength: 70,
  }]],
  themeConfig: {
    colorMode: {defaultMode: 'light', respectPrefersColorScheme: true},
    docs: {sidebar: {autoCollapseCategories: true, hideable: true}},
    tableOfContents: {minHeadingLevel: 2, maxHeadingLevel: 3},
    navbar: {
      title: 'gogo',
      logo: {alt: '', src: 'img/gogo.svg'},
      items: [
        {type: 'docSidebar', sidebarId: 'guideSidebar', label: 'Guide', position: 'left'},
        {type: 'docSidebar', sidebarId: 'adminSidebar', label: 'Admin', position: 'left'},
        {type: 'docSidebar', sidebarId: 'asyncSidebar', label: 'Async', position: 'left'},
        {type: 'docSidebar', sidebarId: 'referenceSidebar', label: 'Reference', position: 'left'},
        {to: '/docs/compatibility', label: '1.0.0-alpha.1', position: 'right', className: 'release-link'},
        {href: 'https://github.com/Newton-School/gogo', label: 'GitHub', position: 'right'},
      ],
    },
    footer: {
      style: 'light',
      copyright: 'Gogo · MIT licensed · Alpha documentation. Read the compatibility notes before deploying.',
    },
    prism: {theme: themes.github, darkTheme: themes.dracula, additionalLanguages: ['go', 'bash', 'sql', 'diff', 'docker', 'toml']},
  },
};
