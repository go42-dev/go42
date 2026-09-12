#!/usr/bin/env node

import {execFileSync} from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {globSync} from 'glob';
import yaml from 'js-yaml';
import {unified} from 'unified';
import remarkParse from 'remark-parse';
import remarkGfm from 'remark-gfm';
import remarkStringify from 'remark-stringify';
import {visit} from 'unist-util-visit';

const rootDirectory = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const markdown = unified().use(remarkParse).use(remarkGfm).use(remarkStringify);
const collections = [
  {name: 'handbook', label: 'Handbook', description: 'The current application and how to work with it.'},
  {name: 'requirements', label: 'Requirements', description: 'Required outcomes, behavior, and verification.'},
  {name: 'decisions', label: 'Decisions', description: 'Significant choices and their reasoning.'},
  {name: 'templates', label: 'Authoring templates', description: 'Starting points for authors; these are not project records.'},
];
const statuses = {
  requirements: ['draft', 'accepted', 'retired'],
  decisions: ['proposed', 'accepted', 'rejected', 'superseded'],
};

function relative(from, to) {
  const result = path.posix.relative(path.posix.dirname(from), to);
  return result.startsWith('.') ? result : `./${result}`;
}

function repositoryPath(root, filename) {
  return path.relative(root, filename).split(path.sep).join('/');
}

function readPage(root, source, target, collection) {
  const text = fs.readFileSync(path.join(root, source), 'utf8');
  const frontMatter = /^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/.exec(text);
  const metadata = frontMatter ? yaml.load(frontMatter[1], {schema: yaml.JSON_SCHEMA}) : {};
  if (!metadata || typeof metadata !== 'object' || Array.isArray(metadata)) {
    throw new Error(`${source}: front matter must be a YAML mapping`);
  }
  const body = frontMatter ? text.slice(frontMatter[0].length) : text;
  return {source, target, collection, metadata, tree: markdown.parse(body)};
}

function validateMetadata(page, ids) {
  const {source, collection, metadata} = page;
  if (typeof metadata.id !== 'string' || !/^[A-Za-z][A-Za-z0-9-]*$/.test(metadata.id)) {
    throw new Error(`${source}: id must contain letters, digits, and hyphens and start with a letter`);
  }
  if (typeof metadata.title !== 'string' || !metadata.title.trim()) {
    throw new Error(`${source}: title is required`);
  }
  if (collection === 'handbook' &&
      (!Number.isSafeInteger(metadata.sidebar_position) || metadata.sidebar_position < 1)) {
    throw new Error(`${source}: handbook sidebar_position must be a positive integer`);
  }
  if (ids.has(metadata.id)) {
    throw new Error(`${source}: duplicate id ${metadata.id} (also in ${ids.get(metadata.id).source})`);
  }
  ids.set(metadata.id, page);
  if (metadata.related !== undefined && (!Array.isArray(metadata.related) ||
      metadata.related.some(id => typeof id !== 'string'))) {
    throw new Error(`${source}: related must be a list of document IDs`);
  }
  if (!statuses[collection]) return;

  const prefix = collection === 'requirements' ? 'REQ' : 'ADR';
  if (!new RegExp(`^${prefix}-[0-9]{3,}$`).test(metadata.id)) {
    throw new Error(`${source}: ${collection} must use ${prefix}-NNN IDs`);
  }
  if (!statuses[collection].includes(metadata.status)) {
    throw new Error(`${source}: status must be one of ${statuses[collection].join(', ')}`);
  }
  if (collection === 'decisions') {
    const date = metadata.date;
    if (typeof date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(date) ||
        !Number.isFinite(Date.parse(date)) || new Date(date).toISOString().slice(0, 10) !== date) {
      throw new Error(`${source}: date must be a real date in YYYY-MM-DD form`);
    }
    if (metadata.status === 'superseded' ? !metadata.superseded_by || typeof metadata.superseded_by !== 'string' :
        metadata.superseded_by !== undefined) {
      throw new Error(`${source}: only a superseded decision must set superseded_by to a replacement ID`);
    }
  }
}

function validateReferences(pages, ids) {
  for (const page of pages) {
    for (const id of page.metadata.related ?? []) {
      if (!ids.has(id)) throw new Error(`${page.source}: unknown related document ${id}`);
    }
    let current = page;
    const seen = new Set([page.metadata.id]);
    while (current.metadata.superseded_by) {
      const id = current.metadata.superseded_by;
      const replacement = ids.get(id);
      if (!replacement || replacement.collection !== 'decisions' ||
          !['accepted', 'superseded'].includes(replacement.metadata.status)) {
        throw new Error(`${current.source}: replacement ${id} must be an accepted or superseded decision`);
      }
      if (seen.has(id)) throw new Error(`${page.source}: decision replacement cycle through ${id}`);
      seen.add(id);
      current = replacement;
    }
  }
}

function localTarget(root, page, url) {
  // Published routes, fragments, and external URLs are checked by the site build or their owner.
  if (!url || /^(?:[A-Za-z][A-Za-z0-9+.-]*:|\/|#)/.test(url)) return undefined;
  const [, pathname, suffix] = /^([^?#]*)(.*)$/.exec(url);
  if (!pathname) return undefined;
  const absolute = path.resolve(root, path.dirname(page.source), decodeURIComponent(pathname));
  const source = repositoryPath(root, absolute);
  if (source === '..' || source.startsWith('../') || path.isAbsolute(source)) {
    throw new Error(`${page.source}: link leaves the repository: ${url}`);
  }
  if (!fs.existsSync(absolute)) throw new Error(`${page.source}: missing link target ${url}`);
  return {source, suffix, directory: fs.statSync(absolute).isDirectory()};
}

function nodeText(node) {
  return node.value ?? (node.children ?? []).map(nodeText).join('');
}

function validateIndex(root, pages, sources) {
  const overview = pages[0];
  const entries = new Map(collections.map(collection => [collection.name, []]));
  const definitions = new Map();
  visit(overview.tree, 'definition', node => {
    definitions.set(node.identifier, node.url);
  });

  let section;
  for (const node of overview.tree.children) {
    if (node.type === 'heading') {
      if (section && node.depth <= section.depth) section = undefined;
      const name = nodeText(node).trim().toLowerCase();
      if (entries.has(name)) section = {name, depth: node.depth};
    }
    if (node.type !== 'table' || !section) continue;
    for (const row of node.children.slice(1)) {
      if (row.children.length !== 2 || !nodeText(row.children[1]).trim()) {
        throw new Error(`${overview.source}: ${section.name} index rows need a document and a nonempty purpose`);
      }
      const links = [];
      visit(row.children[0], ['link', 'linkReference'], link => {
        const url = link.type === 'link' ? link.url : definitions.get(link.identifier);
        const target = localTarget(root, overview, url);
        links.push(target && sources.get(target.source));
      });
      if (links.length !== 1 || !links[0]) {
        throw new Error(`${overview.source}: each ${section.name} index row must link to one documentation source`);
      }
      const page = links[0];
      if (page.collection !== section.name) {
        throw new Error(`${overview.source}: ${page.source} belongs in the ${page.collection} index`);
      }
      const indexed = entries.get(section.name);
      if (indexed.includes(page)) {
        throw new Error(`${overview.source}: duplicate index entry for ${page.source}`);
      }
      indexed.push(page);
    }
  }

  const positions = new Map();
  for (const page of pages.filter(page => page.collection === 'handbook')) {
    const position = page.metadata.sidebar_position;
    if (positions.has(position)) {
      throw new Error(`${page.source}: duplicate handbook sidebar_position ${position} (also in ${positions.get(position)})`);
    }
    positions.set(position, page.source);
  }
  for (const {name} of collections) {
    const indexed = entries.get(name);
    const missing = pages.filter(page => page.collection === name && !indexed.includes(page));
    if (missing.length) {
      throw new Error(`${overview.source}: missing ${name} index entries: ${missing.map(page => page.source).join(', ')}`);
    }
    if (name === 'templates') continue;
    const order = page => name === 'handbook' ? page.metadata.sidebar_position : BigInt(page.metadata.id.slice(4));
    for (let index = 1; index < indexed.length; index++) {
      if (order(indexed[index - 1]) >= order(indexed[index])) {
        const field = name === 'handbook' ? 'sidebar_position' : 'record number';
        throw new Error(`${overview.source}: ${name} index must follow ascending ${field} order`);
      }
    }
  }
}

export function loadDocumentation(root = rootDirectory) {
  const pages = [readPage(root, 'docs/README.md', 'index.md', 'overview')];
  for (const collection of collections) {
    const files = globSync('**/*.md', {cwd: path.join(root, 'docs', collection.name), nodir: true}).sort();
    for (const file of files) {
      pages.push(readPage(root, `docs/${collection.name}/${file}`, `${collection.name}/${file}`, collection.name));
    }
  }
  const ids = new Map();
  const sources = new Map();
  for (const page of pages) {
    validateMetadata(page, ids);
    sources.set(page.source, page);
    visit(page.tree, ['link', 'image', 'definition'], node => {
      localTarget(root, page, node.url);
    });
  }
  validateReferences(pages, ids);
  validateIndex(root, pages, sources);
  // Entry points are read from the checkout, so their links must work there too.
  for (const source of ['README.md', 'AGENTS.md', 'CLAUDE.md', 'GEMINI.md', '.github/copilot-instructions.md']) {
    if (!fs.existsSync(path.join(root, source))) continue;
    const tree = markdown.parse(fs.readFileSync(path.join(root, source), 'utf8'));
    visit(tree, ['link', 'image', 'definition'], node => {
      localTarget(root, {source}, node.url);
    });
  }
  return {root, pages, ids, sources};
}

function git(root, args) {
  try {
    return execFileSync('git', args, {cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore']}).trim();
  } catch {
    return '';
  }
}

export function buildContext(root = rootDirectory, env = process.env) {
  let repository = env.DOCS_REPOSITORY_URL;
  if (!repository && env.GITHUB_REPOSITORY) {
    repository = `${env.GITHUB_SERVER_URL || 'https://github.com'}/${env.GITHUB_REPOSITORY}`;
  }
  if (!repository) {
    const origin = git(root, ['config', '--get', 'remote.origin.url']);
    if (/^https?:\/\//.test(origin)) repository = origin;
    else if (origin.startsWith('ssh://')) {
      const remote = new URL(origin);
      repository = `https://${remote.hostname}${remote.pathname}`;
    } else {
      const ssh = /^(?:[^@]+@)?([^:]+):(.+)$/.exec(origin);
      if (ssh && !origin.startsWith('/')) repository = `https://${ssh[1]}/${ssh[2]}`;
    }
  }
  if (repository) {
    const url = new URL(repository);
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) {
      throw new Error('DOCS_REPOSITORY_URL must be an HTTP(S) repository URL without credentials');
    }
    repository = url.href.replace(/\/$/, '').replace(/\.git$/, '');
  }
  return {
    repository,
    revision: env.DOCS_REVISION || git(root, ['rev-parse', 'HEAD']) || 'HEAD',
    dirty: Boolean(git(root, ['status', '--porcelain'])),
  };
}

function sourceURL(context, source, directory = false) {
  if (!context.repository) {
    throw new Error('Set DOCS_REPOSITORY_URL to the repository web URL when no GitHub origin can be detected');
  }
  const filename = source.split('/').map(encodeURIComponent).join('/');
  return `${context.repository}/${directory ? 'tree' : 'blob'}/${encodeURIComponent(context.revision)}/${filename}`;
}

function renderPage(bundle, page, context) {
  const imageDefinitions = new Set();
  visit(page.tree, 'imageReference', node => {
    imageDefinitions.add(node.identifier);
  });
  visit(page.tree, ['link', 'image', 'definition'], node => {
    const target = localTarget(bundle.root, page, node.url);
    if (!target) return;
    const destination = bundle.sources.get(target.source) ||
      (target.directory ? bundle.sources.get(`${target.source}/README.md`) : undefined);
    if (destination) {
      node.url = relative(page.target, destination.target) + target.suffix;
    } else if (node.type === 'image' || (node.type === 'definition' && imageDefinitions.has(node.identifier))) {
      if (target.directory) throw new Error(`${page.source}: image must reference a file: ${node.url}`);
      node.url = relative(`pages/docs/${page.target}`, target.source) + target.suffix;
    } else {
      node.url = sourceURL(context, target.source, target.directory) + target.suffix;
    }
  });

  const notices = [];
  if (page.collection === 'templates') {
    notices.push('**Authoring resource.** Copy a template into the appropriate collection and replace its prompts.');
  } else if (statuses[page.collection]) {
    notices.push(`**Status:** ${page.metadata.status}`);
    if (page.metadata.date) notices.push(`**Recorded:** ${page.metadata.date}`);
    if (page.metadata.superseded_by) {
      const next = bundle.ids.get(page.metadata.superseded_by);
      notices.push(`**Replaced by:** [${next.metadata.id}](${relative(page.target, next.target)})`);
    }
  }
  if (page.metadata.related?.length) {
    const links = page.metadata.related.map(id => `[${id}](${relative(page.target, bundle.ids.get(id).target)})`);
    notices.push(`**Related:** ${links.join(', ')}`);
  }
  const heading = page.tree.children.findIndex(node => node.type === 'heading' && node.depth === 1);
  const noticeTree = markdown.parse(notices.map(notice => `> ${notice}`).join('\n>\n'));
  page.tree.children.splice(heading + 1, 0, ...noticeTree.children);
  const source = `[${page.source}](${sourceURL(context, page.source)})`;
  const revision = `\`${context.revision.slice(0, 12)}\`${context.dirty ? ' with local changes' : ''}`;
  const footer = markdown.parse(`\n---\n\nSource: ${source}. Revision: ${revision}.\n`);
  page.tree.children.push(...footer.children);
  return `---\n${yaml.dump(page.metadata, {lineWidth: 120, noRefs: true})}---\n\n${markdown.stringify(page.tree)}`;
}

export function assemble({root = rootDirectory, context = buildContext(root)} = {}) {
  // Validate all sources before replacing the generated directory.
  const bundle = loadDocumentation(root);
  const rendered = bundle.pages.map(page => ({target: page.target, text: renderPage(bundle, page, context)}));
  const output = path.join(root, 'pages/docs');
  fs.rmSync(output, {recursive: true, force: true});
  fs.mkdirSync(output, {recursive: true});
  for (const page of rendered) {
    const target = path.join(output, page.target);
    fs.mkdirSync(path.dirname(target), {recursive: true});
    fs.writeFileSync(target, page.text);
  }
  for (const [index, collection] of collections.entries()) {
    const directory = path.join(output, collection.name);
    fs.mkdirSync(directory, {recursive: true});
    const category = {
      label: collection.label, position: index + 2, collapsed: true,
      link: {type: 'generated-index', description: collection.description},
    };
    fs.writeFileSync(path.join(directory, '_category_.json'), `${JSON.stringify(category, null, 2)}\n`);
  }
  return rendered.length;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.includes('--check')) {
      const bundle = loadDocumentation();
      console.log(`Validated ${bundle.pages.length} documentation sources, metadata, index, and local file links.`);
    } else {
      console.log(`Assembled ${assemble()} documentation pages.`);
    }
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
