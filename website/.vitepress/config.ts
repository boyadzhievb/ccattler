import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'CCattler',
  description: 'Fact-based container orchestrator — a Kubernetes alternative built on facts, rules, and reconciliation.',
  outDir: '../docs',
  base: '/',
  cleanUrls: true,

  head: [
    ['meta', { name: 'theme-color', content: '#6378ff' }],
  ],

  themeConfig: {
    logo: { light: '/logo.svg', dark: '/logo.svg' },
    siteTitle: 'CCattler',

    nav: [
      { text: 'Getting Started', link: '/getting-started' },
      { text: 'Concepts', link: '/concepts/' },
      { text: 'Guides', link: '/guides/' },
      { text: 'Reference', link: '/reference/' },
      { text: 'Operations', link: '/operations/' },
      { text: 'v1.0.0-beta', link: '/changelog' },
    ],

    sidebar: {
      '/concepts/': [
        {
          text: 'Concepts',
          items: [
            { text: 'Overview', link: '/concepts/' },
            { text: 'Facts & State', link: '/concepts/facts' },
            { text: 'Reconciliation', link: '/concepts/reconciliation' },
            { text: 'Controllers', link: '/concepts/controllers' },
            { text: 'Networking', link: '/concepts/networking' },
            { text: 'Security', link: '/concepts/security' },
          ],
        },
      ],
      '/guides/': [
        {
          text: 'Guides',
          items: [
            { text: 'Overview', link: '/guides/' },
            { text: 'First Service', link: '/guides/first-service' },
            { text: 'Multi-Tenancy', link: '/guides/multi-tenancy' },
            { text: 'Placement', link: '/guides/placement' },
          ],
        },
      ],
      '/reference/': [
        {
          text: 'Reference',
          items: [
            { text: 'Overview', link: '/reference/' },
            { text: 'CLI Commands', link: '/reference/cli' },
            { text: 'DSL Grammar', link: '/reference/dsl' },
            { text: 'Architecture', link: '/reference/architecture' },
            { text: 'etcd Schema', link: '/reference/etcd-schema' },
          ],
        },
      ],
      '/operations/': [
        {
          text: 'Operations',
          items: [
            { text: 'Overview', link: '/operations/' },
            { text: 'Upgrading', link: '/operations/upgrading' },
            { text: 'Backup & Restore', link: '/operations/backup' },
            { text: 'Troubleshooting', link: '/operations/troubleshooting' },
          ],
        },
      ],
    },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/boyadzhievb/ccattler' },
    ],

    editLink: {
      pattern: 'https://github.com/boyadzhievb/ccattler/edit/master/website/:path',
    },

    search: {
      provider: 'local',
    },

    footer: {
      message: 'Released under the GPL-3.0 License.',
      copyright: 'Container Cattler — built from first principles.',
    },
  },
})
