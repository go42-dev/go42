import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import {assemble, buildContext, loadDocumentation} from './assemble.mjs';

const context = {repository: 'https://github.com/example/application', revision: 'abc123', dirty: false};

function write(root, filename, content) {
  const target = path.join(root, filename);
  fs.mkdirSync(path.dirname(target), {recursive: true});
  fs.writeFileSync(target, content);
}

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'go42-docs-'));
  t.after(() => fs.rmSync(root, {recursive: true, force: true}));
  write(root, 'docs/README.md', '---\nid: home\ntitle: Overview\nslug: /\n---\n\n# Overview\n');
  write(root, 'docs/handbook/conventions.md',
    '---\nid: conventions\ntitle: Conventions\n---\n\n# Conventions\n\n[Profile](project.md)\n');
  write(root, 'docs/handbook/project.md', '---\nid: project\ntitle: Profile\n---\n\n# Profile\n');
  write(root, 'docs/requirements/001-documentation.md',
    '---\nid: REQ-001\ntitle: Documentation\nstatus: draft\nrelated: [ADR-001]\n---\n\n# Documentation\n');
  write(root, 'docs/decisions/001-model.md',
    '---\nid: ADR-001\ntitle: Model\nstatus: proposed\ndate: 2026-09-11\n---\n\n# Model\n');
  write(root, 'docs/templates/decision.md',
    '---\nid: template-decision\ntitle: Decision template\nstatus: proposed\ndate: YYYY-MM-DD\n---\n\n# Template\n');
  return root;
}

function change(root, filename, from, to) {
  const target = path.join(root, filename);
  fs.writeFileSync(target, fs.readFileSync(target, 'utf8').replace(from, to));
}

test('publishes source documents without changing sources', t => {
  const root = fixture(t);
  const homepage = path.join(root, 'docs/README.md');
  fs.appendFileSync(homepage, '\n[Conventions](handbook/conventions.md) and [requirement](requirements/001-documentation.md).\n');
  const before = fs.readFileSync(homepage, 'utf8');
  write(root, 'pages/docs/architecture/stale.md', '# Old generated file\n');

  assert.equal(assemble({root, context}), 6);
  assert.equal(fs.readFileSync(homepage, 'utf8'), before);
  const output = fs.readFileSync(path.join(root, 'pages/docs/index.md'), 'utf8');
  assert.match(output, /\[Conventions\]\(\.\/handbook\/conventions\.md\)/);
  assert.match(output, /\[requirement\]\(\.\/requirements\/001-documentation\.md\)/);
  assert.match(output, /example\/application\/blob\/abc123\/docs\/README.md/);
  assert.equal(fs.existsSync(path.join(root, 'pages/docs/architecture/stale.md')), false);
  const conventions = fs.readFileSync(path.join(root, 'pages/docs/handbook/conventions.md'), 'utf8');
  assert.match(conventions, /\[Profile\]\(\.\/project\.md\)/);
  const requirement = fs.readFileSync(path.join(root, 'pages/docs/requirements/001-documentation.md'), 'utf8');
  assert.match(requirement, /\*\*Status:\*\* draft/);
  assert.match(requirement, /\[ADR-001\]\(\.\.\/decisions\/001-model\.md\)/);
  const template = fs.readFileSync(path.join(root, 'pages/docs/templates/decision.md'), 'utf8');
  assert.match(template, /\*\*Authoring resource\./);
  assert.doesNotMatch(template, /\*\*Status:/);
});

test('resolves table and reference links, source files, and images but leaves code examples alone', t => {
  const root = fixture(t);
  write(root, 'Makefile', 'help:\n\t@echo help\n');
  write(root, 'pages/static/img/diagram.svg', '<svg xmlns="http://www.w3.org/2000/svg"/>\n');
  fs.appendFileSync(path.join(root, 'docs/handbook/project.md'), [
    '\n| Resource | Link |', '| --- | --- |', '| Rules | [Rules](conventions.md) |',
    '\n[Build](../../Makefile#L1)', '\n![Architecture][diagram]',
    '\n[diagram]: ../../pages/static/img/diagram.svg', '\n[Requirement][req]',
    '\n![Overview](../../pages/static/img/diagram.svg)',
    '\n[req]: ../requirements/001-documentation.md',
    '\n```markdown', '[Example](missing-example.md)', '```', '',
  ].join('\n'));

  assemble({root, context: {...context, dirty: true}});
  const output = fs.readFileSync(path.join(root, 'pages/docs/handbook/project.md'), 'utf8');
  assert.match(output, /\[Rules\]\(\.\/conventions\.md\)/);
  assert.match(output, /example\/application\/blob\/abc123\/Makefile#L1/);
  assert.match(output, /\[req\]: \.\.\/requirements\/001-documentation\.md/);
  assert.match(output, /\[diagram\]: \.\.\/\.\.\/static\/img\/diagram\.svg/);
  assert.match(output, /!\[Overview\]\(\.\.\/\.\.\/static\/img\/diagram\.svg\)/);
  assert.match(output, /\[Example\]\(missing-example\.md\)/);
  assert.match(output, /with local changes/);
  assert.equal(fs.existsSync(path.join(root, 'pages/docs/_assets')), false);
});

for (const [name, filename, from, to, error] of [
  ['missing title', 'docs/handbook/project.md', 'title: Profile', 'summary: Profile', /title is required/],
  ['duplicate IDs', 'docs/requirements/001-documentation.md', 'id: REQ-001', 'id: project', /duplicate id/],
  ['invalid requirement ID', 'docs/requirements/001-documentation.md', 'id: REQ-001', 'id: feature', /REQ-NNN/],
  ['invalid status', 'docs/requirements/001-documentation.md', 'status: draft', 'status: implemented', /status must be/],
  ['unknown related ID', 'docs/requirements/001-documentation.md', '[ADR-001]', '[ADR-099]', /unknown related/],
  ['invalid related type', 'docs/requirements/001-documentation.md', '[ADR-001]', 'ADR-001', /related must be a list/],
  ['invalid date', 'docs/decisions/001-model.md', '2026-09-11', '2026-02-30', /date must be a real date/],
  ['missing replacement', 'docs/decisions/001-model.md', 'status: proposed', 'status: superseded', /superseded_by/],
]) {
  test(`rejects ${name}`, t => {
    const root = fixture(t);
    change(root, filename, from, to);
    assert.throws(() => loadDocumentation(root), error);
  });
}

test('rejects broken links in tables, reference definitions, and agent entry points', t => {
  const root = fixture(t);
  const filename = path.join(root, 'docs/handbook/project.md');
  const original = fs.readFileSync(filename, 'utf8');
  for (const badLink of [
    '\n| Link |\n| --- |\n| [Missing](missing.md) |\n',
    '\n[Missing][target]\n\n[target]: missing.md\n',
  ]) {
    fs.writeFileSync(filename, original + badLink);
    assert.throws(() => loadDocumentation(root), /missing link target missing.md/);
  }
  fs.writeFileSync(filename, original);
  write(root, 'AGENTS.md', '# Instructions\n\n[Policy](docs/missing.md)\n');
  assert.throws(() => loadDocumentation(root), /AGENTS.md: missing link target/);
});

test('rejects a link outside the checkout and leaves the last assembled site intact on validation failure', t => {
  const root = fixture(t);
  assemble({root, context});
  const generated = path.join(root, 'pages/docs/index.md');
  const previous = fs.readFileSync(generated, 'utf8');
  fs.appendFileSync(path.join(root, 'docs/handbook/project.md'), '\n[Outside](../../../outside.md)\n');
  assert.throws(() => assemble({root, context}), /link leaves the repository/);
  assert.equal(fs.readFileSync(generated, 'utf8'), previous);
});

test('validates replacement decisions and rejects cycles or a proposed replacement', t => {
  const root = fixture(t);
  change(root, 'docs/decisions/001-model.md', 'status: proposed', 'status: superseded\nsuperseded_by: ADR-002');
  write(root, 'docs/decisions/002-replacement.md',
    '---\nid: ADR-002\ntitle: Replacement\nstatus: accepted\ndate: 2026-09-11\n---\n\n# Replacement\n');
  assemble({root, context});
  const output = fs.readFileSync(path.join(root, 'pages/docs/decisions/001-model.md'), 'utf8');
  assert.match(output, /\*\*Replaced by:\*\* \[ADR-002\]\(\.\/002-replacement.md\)/);

  change(root, 'docs/decisions/002-replacement.md', 'status: accepted', 'status: proposed');
  assert.throws(() => loadDocumentation(root), /must be an accepted or superseded decision/);
  change(root, 'docs/decisions/002-replacement.md', 'status: proposed', 'status: superseded\nsuperseded_by: ADR-001');
  assert.throws(() => loadDocumentation(root), /replacement cycle/);
});

test('uses the fork repository for standard Git remotes and the CI repository during publishing', t => {
  const root = fixture(t);
  execFileSync('git', ['init', '--quiet'], {cwd: root});
  for (const origin of [
    'https://github.com/example/application.git',
    'git@github.com:example/application.git',
    'ssh://git@github.com/example/application.git',
  ]) {
    execFileSync('git', ['config', 'remote.origin.url', origin], {cwd: root});
    assert.equal(buildContext(root, {}).repository, context.repository);
  }
  const published = buildContext(root, {
    GITHUB_SERVER_URL: 'https://github.example.com',
    GITHUB_REPOSITORY: 'team/service',
    DOCS_REVISION: 'v1.0.0',
  });
  assert.equal(published.repository, 'https://github.example.com/team/service');
  assert.equal(published.revision, 'v1.0.0');
});
