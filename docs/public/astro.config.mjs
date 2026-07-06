import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://newton-school.github.io/gogo',
  base: '/gogo',
  integrations: [
    starlight({
      title: 'Gogo',
      description: 'A Django-inspired backend framework for Go.',
      favicon: '/favicon.svg',
      logo: {
        src: './src/assets/gogo-logo.svg',
        alt: 'Gogo',
      },
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/Newton-School/gogo',
        },
      ],
      customCss: ['./src/styles/custom.css'],
      sidebar: [
        {
          label: 'Start',
          items: [
            { label: 'Overview', slug: 'index' },
            { label: 'Installation', slug: 'getting-started/installation' },
            { label: 'Create a Project', slug: 'getting-started/create-project' },
          ],
        },
        {
          label: 'Framework',
          items: [
            { label: 'Feature Map', slug: 'framework/features' },
            { label: 'Package Index', slug: 'framework/package-index' },
            { label: 'Contrib and Cross-Cutting', slug: 'framework/contrib-cross-cutting' },
            { label: 'Project Layout', slug: 'framework/project-layout' },
            { label: 'Settings', slug: 'framework/settings' },
          ],
        },
        {
          label: 'Data Layer',
          items: [
            { label: 'Models and Fields', slug: 'data/models-fields' },
            { label: 'ORM Queries', slug: 'data/orm-queries' },
            { label: 'Migrations', slug: 'data/migrations' },
          ],
        },
        {
          label: 'Web',
          items: [
            { label: 'HTTP', slug: 'web/http' },
            { label: 'Admin', slug: 'web/admin' },
            { label: 'API', slug: 'web/api' },
            { label: 'Auth', slug: 'web/auth' },
            { label: 'Forms, Templates, Static', slug: 'web/forms-templates-static' },
          ],
        },
        {
          label: 'Background Work',
          items: [{ label: 'Queues and Workers', slug: 'background/queues-workers' }],
        },
        {
          label: 'Operations',
          items: [
            { label: 'Testing and CLI', slug: 'operations/testing-cli' },
            { label: 'Deployment', slug: 'operations/deployment' },
          ],
        },
      ],
    }),
  ],
});
