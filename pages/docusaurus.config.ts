import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const config: Config = {
  title: process.env.DOCUSAURUS_TITLE || 'go42',
  url: process.env.DOCUSAURUS_URL || 'https://go42-dev.github.io',
  baseUrl: process.env.DOCUSAURUS_BASE_URL || '/go42/',
  favicon: 'img/go42-logo.svg',
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  markdown: {
    format: 'detect',
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },
  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },
  // @see https://docusaurus.io/docs/api/docusaurus-config#future
  future: {
    v4: true,
  },
  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: '/',
          sidebarCollapsible: true,
          sidebarCollapsed: false,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
    [
      'redocusaurus',
      {
        specs: [
          {
            id: 'api-v1',
            spec: '../api/openapi/v1/.combined.yaml',
            route: '/api/v1',
          }
        ],
        theme: {
          primaryColor: '#0969da',
        },
      },
    ],
  ],
  themes: [
    '@docusaurus/theme-mermaid',
    [
      require.resolve("@easyops-cn/docusaurus-search-local"),
      {
        hashed: true,
        language: ["en"],
        indexDocs: true,
        indexBlog: false,
        docsRouteBasePath: "/",
        highlightSearchTermsOnTargetPage: true,
      },
    ],
  ],
  themeConfig: {
    image: 'img/go42-logo.svg',
    docs: {
      sidebar: {
        hideable: true,
        autoCollapseCategories: true,
      },
    },
    navbar: {
      title: 'Documentation',
      logo: {
        alt: 'logo',
        src: 'img/go42-logo.svg',
      },
      items: [
        {
          type: 'dropdown',
          label: 'OpenAPI',
          position: 'left',
          items: [
            {
              label: 'Version 1',
              to: '/api/v1',
            }
          ],
        },
        {
          type: 'search',
          position: 'right',
        },
      ],
    },
    // prismThemes.github (light)
    // prismThemes.dracula (dark)
    // prismThemes.duotoneDark
    // prismThemes.duotoneLight
    // prismThemes.nightOwl
    // prismThemes.oceanicNext
    // prismThemes.okaidia
    // prismThemes.palenight
    // prismThemes.shadesOfPurple
    // prismThemes.synthwave84
    // prismThemes.ultramin
    // prismThemes.vsDark
    // prismThemes.vsLight
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['go', 'bash', 'json', 'yaml', 'docker', 'makefile'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
