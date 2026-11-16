import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile, readdir} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {renderMarkdown, slugify} from '../scripts/markdown.mjs';
import {renderPage} from '../src/templates.mjs';

const root = new URL('../../', import.meta.url);
const site = new URL('../', import.meta.url);
const read = (name, base = site) => readFile(new URL(name, base), 'utf8');
const config = JSON.parse(await read('site.config.json'));
const pages = config.navigation.flatMap(group => group.items);
const hashes = {css: 'test', js: 'test', init: 'test'};
const home = () => renderPage({config, page: {title: '首页', slug: ''}, home: true, hashes});

test('TGCloud package, site and Python source share version 0.0.1', async () => {
  assert.equal(config.name, 'TGCloud');
  assert.equal(config.version, '0.0.1');
  const source = await read('src/tgcloud/version.py', root);
  assert.deepEqual([...source.matchAll(/^VERSION\s*=\s*"([^"]+)"\r?$/gm)].map(m => m[1]), [config.version]);
  const pkg = JSON.parse(await read('package.json'));
  const lock = JSON.parse(await read('package-lock.json'));
  assert.equal(pkg.name, 'tgcloud-docs');
  for (const value of [pkg.version, lock.version, lock.packages[''].version]) assert.equal(value, config.version);
  assert.match(await read('pyproject.toml', root), /name = "tgcloud"/);
});

test('unpublished metadata has no fabricated release or production hostname', () => {
  assert.equal(config.releaseStatus, 'unreleased');
  assert.equal(config.publishedVersion, null);
  assert.equal(config.siteUrl, '');
  assert.equal(config.repo, 'https://github.com/LuoPoJunZi/TGCloud');
  assert.equal(new Date(config.verifiedAt).toISOString().slice(0, 10), config.verifiedAt);
  assert.doesNotMatch(home(), /releases\/(?:latest|download|tag)|rel="canonical"/);
  assert.ok(home().includes('尚未发布'));
});

test('current navigation and homepage expose one CloudDrive2 gateway', async () => {
  assert.equal(new Set(pages.map(page => page.slug)).size, pages.length);
  for (const page of pages) {
    assert.doesNotMatch(page.slug, /openlist|editions/);
    const article = await read(`docs/${page.slug}.md`);
    assert.equal(renderMarkdown(article).headings.filter(h => h.level === 1).length, 1, page.slug);
  }
  const html = home();
  assert.ok(html.includes('/deploy/clouddrive2/'));
  assert.doesNotMatch(html, /TG2Cloud|tg2cloud|OpenList|双版本|版本选择|选择 Edition|deploy\/openlist/);
  for (const name of ['docs/deploy/openlist.md', 'docs/guide/editions.md']) {
    await assert.rejects(read(name), {code: 'ENOENT'});
  }
});

test('installation pages agree on complete local source entry points', async () => {
  for (const [name, base] of [['README.md', root], ['docs/install.md', root], ['docs/guide/quick-start.md', site]]) {
    const text = await read(name, base);
    for (const command of ['sudo bash install.sh', 'sudo bash install-lan.sh']) assert.ok(text.includes(command), name);
    assert.doesNotMatch(text, /bash <\(curl|releases\/download\/|Deployer\.exe/);
  }
  const compose = await read('docs/compose-guide.md', root);
  assert.ok(compose.includes('scripts/build_compose.py --output'));
  assert.ok(compose.includes('sudo bash manage.sh'));
  assert.ok(compose.includes('tgcloud-clouddrive2-bot'));
  assert.doesNotMatch(compose, /选择 (?:Edition|OpenList)|compose-openlist/);
});

test('download and changelog pages distinguish local work from published assets', async () => {
  for (const name of ['docs/download.md', 'docs/changelog.md', 'docs/sources.md']) {
    const text = await read(name);
    assert.ok(text.includes('0.0.1'), name);
    assert.doesNotMatch(text, /releases\/(?:latest|tag|download)|v1\.[0-9]/, name);
  }
  const history = await read('docs/changelog.md');
  assert.match(history, /^## TGCloud v0\.0\.1$/m);
  const headings = renderMarkdown(history).headings;
  const html = renderPage({config: {...config, versionHistory: headings.filter(h => h.level === 2)
    .map(h => ({version: '0.0.1', id: h.id}))}, page: {slug: '', title: '首页'}, home: true, hashes});
  assert.ok(html.includes('/changelog/#' + slugify('TGCloud v0.0.1')));
});

test('every article points to its actual source and an existing project reference', async () => {
  for (const page of pages) {
    const parsed = renderMarkdown(await read(`docs/${page.slug}.md`));
    const html = renderPage({config, page, body: parsed.html, headings: parsed.headings, hashes});
    assert.ok(html.includes(`${config.repo}/blob/main/website/docs/${page.slug}.md`), page.slug);
    const reference = html.match(/class="reference-source" href="[^"]+\/blob\/main\/([^"]+)"/);
    assert.ok(reference, page.slug);
    assert.ok(await read(reference[1], root), page.slug);
    if (parsed.headings.some(h => h.level === 2)) assert.ok(html.includes('class="mobile-toc"'), page.slug);
  }
});

test('management documentation keeps every current menu item', async () => {
  const source = await read('src/tgcloud/cli/management.py', root);
  const items = source.match(/MENU_ITEMS = \(([\s\S]*?)\r?\n\)/)?.[1];
  assert.ok(items);
  const entries = [...items.matchAll(/\((\d+), "([^"]+)"\)/g)].map(m => [m[1], m[2]]);
  assert.deepEqual(entries.map(([n]) => n), [...Array.from({length: 16}, (_, i) => String(i + 1)), '0']);
  for (const [name, base] of [['docs/operations-guide.md', root], ['docs/operations/maintenance.md', site]]) {
    const guide = await read(name, base);
    for (const [number, label] of entries) assert.ok(guide.includes(`${number}. ${label}`), `${name}: ${label}`);
  }
});

test('backup instructions preserve current submenu operations and recovery boundaries', async () => {
  const source = await read('src/tgcloud/cli/management.py', root);
  const block = source.split('    def backups(')[1]?.split('    def remove(')[0];
  assert.ok(block);
  const entries = [...block.matchAll(/\((\d+), "([^"]+)"\)/g)].map(m => `${m[1]}. ${m[2]}`);
  assert.ok(entries.length >= 7);
  for (const [name, base] of [['docs/backup-and-recovery.md', root], ['docs/operations/backup.md', site]]) {
    const guide = await read(name, base);
    for (const label of entries) assert.ok(guide.includes(label), `${name}: ${label}`);
    for (const value of ['Session', '备份', '不等于生产恢复']) assert.ok(guide.includes(value), name);
  }
});

test('canonical brand copies match byte for byte and the wordmark uses TGCloud', async () => {
  const pairs = [['tgcloud.ico', 'favicon.ico'], ...['tgcloud-icon.svg', 'tgcloud-icon-32.png',
    'tgcloud-icon-64.png', 'tgcloud-icon-256.png'].map(name => [name, `assets/${name}`])];
  const hash = bytes => createHash('sha256').update(bytes).digest('hex');
  for (const [canonical, copy] of pairs) {
    assert.equal(hash(await readFile(new URL(`public/${copy}`, site))),
      hash(await readFile(new URL(`assets/brand/${canonical}`, root))), copy);
  }
  const wordmark = await read('assets/brand/tgcloud-logo.svg', root);
  assert.ok(wordmark.includes('TGCloud'));
  assert.doesNotMatch(wordmark, /TG2Cloud|tg2cloud/);
  for (const base of ['src/', 'assets/brand/', 'website/public/assets/']) {
    assert.ok(!(await readdir(new URL(base, root))).some(name => /tg2cloud/i.test(name)), base);
  }
});

test('historical records remain indexed and byte-verifiable in their archive', async () => {
  const index = JSON.parse(await read('archive/project-history/record-index.json', root));
  assert.equal(index.project, 'TGCloud');
  assert.equal(index.source_project, 'TG2Cloud');
  assert.ok(index.records.length >= 81);
  for (const record of index.records) {
    assert.ok(record.path.startsWith('archive/project-history/'));
    assert.doesNotMatch(record.path, /tg2cloud/i);
    const bytes = await readFile(new URL(record.path, root));
    assert.equal(createHash('sha256').update(bytes).digest('hex'), record.sha256, record.path);
  }
  for (const name of ['README.md', 'README_EN.md', 'docs/README.md']) {
    assert.ok((await read(name, root)).includes('archive/README.md'), name);
  }
});

test('current legal and source notes match the new project without changing copyright', async () => {
  const notices = await read('THIRD_PARTY_NOTICES.md');
  assert.ok(notices.includes(`TGCloud v${config.version}`));
  assert.ok(notices.includes(config.verifiedAt));
  assert.ok(notices.includes('whyhhh20/TG115'));
  assert.doesNotMatch(notices, /releases\/tag\/|TGCloud v1\./);
  assert.ok((await read('LICENSE', root)).includes('TG115 Contributors'));
});

test('legacy anchor support still rejects injected and missing targets', () => {
  assert.throws(() => renderMarkdown('# Title', {legacyAnchors: {'bad" onclick="x': 'Title'}}), /Invalid legacy anchor/);
  assert.throws(() => renderMarkdown('# Title', {legacyAnchors: {old: 'Absent'}}), /Missing legacy anchor target/);
  const native = renderMarkdown('# Title\n\n## Old', {legacyAnchors: {old: 'Title'}});
  assert.equal([...native.html.matchAll(/id="old"/g)].length, 1);
  const before = renderMarkdown('# Title\n\n## Heading');
  const after = renderMarkdown('# Title\n\n## Heading', {legacyAnchors: {previous: 'Heading'}});
  assert.deepEqual(after.headings, before.headings);
  assert.ok(after.html.includes('id="previous"'));
});

test('error page retains navigation and search without an invented canonical URL', () => {
  const html = renderPage({config, page: {title: '未找到', slug: ''}, notFound: true, hashes});
  assert.ok(html.includes('<meta name="robots" content="noindex">'));
  assert.ok(html.includes('href="/">返回文档首页'));
  assert.match(html, /<button[^>]*data-open-search>搜索文档<\/button>/);
  assert.doesNotMatch(html, /rel="canonical"|pages\.dev/);
});
