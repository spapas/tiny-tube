// Focused behavior tests for embedded UI scripts; no packages or browser needed.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../web/static/app.js'), 'utf8');
const i18nSource = fs.readFileSync(path.join(__dirname, '../web/static/i18n.js'), 'utf8');
const localePath = path.join(__dirname, '../web/locales');
const catalogs = Object.fromEntries(fs.readdirSync(localePath).filter(name => name.endsWith('.json')).map(name => [name.slice(0,-5), JSON.parse(fs.readFileSync(path.join(localePath, name), 'utf8'))]));

function browserMessages(lang, partial = catalogs[lang] || {}) {
  return Object.fromEntries(Object.entries({ ...catalogs.en, ...partial }).map(([id, message]) => [id, { ...message, language: Object.hasOwn(partial, id) ? lang : 'en' }]));
}

function boot({ forms = [], metadata = null, share = null, lang = 'el' } = {}) {
  const messages = [], copied = [], error = { hidden: true, textContent: '' };
  let copyHandler;
  const document = {
    documentElement: { lang, dataset: { theme: 'light' } },
    querySelectorAll(selector) { return selector === '[data-delete-video]' ? forms : []; },
    querySelector(selector) {
      if (selector === '[data-metadata-form]') return metadata;
      if (selector === '[data-share-url]') return share;
      if (selector === '[data-copy-link]' && share) return { addEventListener(_, fn) { copyHandler = fn; } };
      if (selector === '[data-share-status]') return { textContent: '' };
      return null;
    },
    getElementById() { return error; }
  };
  const window = { location: { href: 'http://review.test/watch?id=video', origin: 'http://review.test' }, addEventListener() {}, confirm(message) { messages.push(message); return false; } };
  window.tinyTubeMessages = browserMessages(lang);
  const context = vm.createContext({ document, window, URL, TextEncoder, navigator: { clipboard: { async writeText(value) { copied.push(value); } } } });
  vm.runInContext(i18nSource, context);
  vm.runInContext(source, context);
  return { messages, copied, error, translate: window.tinyTubeTranslate, copy: () => copyHandler() };
}

test('every user/list deletion receives the correct confirmation', () => {
  const forms = ['/api/admin/users/delete', '/api/admin/lists/delete', '/api/delete'].map(action => ({ action, addEventListener(_, fn) { this.submit = fn; } }));
  const ui = boot({ forms });
  for (const form of forms) {
    let prevented = false;
    form.submit({ preventDefault() { prevented = true; } });
    assert.equal(prevented, true);
  }
  assert.match(ui.messages[0], /χρήστης/);
  assert.match(ui.messages[1], /λίστα/);
  assert.match(ui.messages[2], /βίντεο/);
});

test('edit validates UTF-8 bytes and focuses the field without losing data', () => {
  let focused = false, prevented = false;
  const metadata = { elements: { title: { value: 'Ω'.repeat(300), focus() { focused = true; } }, desc: { value: '' }, category: { value: '' } }, addEventListener(_, fn) { this.submit = fn; } };
  const ui = boot({ metadata });
  metadata.submit({ currentTarget: metadata, preventDefault() { prevented = true; } });
  assert.equal(prevented, true); assert.equal(focused, true); assert.equal(ui.error.hidden, false);
  assert.equal(metadata.elements.title.value.length, 300);
});

test('sharing uses the server-provided capability URL', async () => {
  const share = { dataset: { sharePath: '/watch?id=video&key=lt-capability' } };
  const ui = boot({ share });
  await ui.copy();
  assert.equal(ui.copied[0], 'http://review.test/watch?id=video&key=lt-capability');
});

test('English confirmations and edit validation use the shared catalog', () => {
  const forms = ['/api/admin/users/delete', '/api/admin/lists/delete', '/api/delete'].map(action => ({ action, addEventListener(_, fn) { this.submit = fn; } }));
  const metadata = { elements: { title: { value: 'Ω'.repeat(300), focus() {} } }, addEventListener(_, fn) { this.submit = fn; } };
  const ui = boot({ forms, metadata, lang: 'en' });
  for (const form of forms) form.submit({ preventDefault() {} });
  assert.match(ui.messages[0], /delete this user/);
  assert.match(ui.messages[1], /delete this locked list/);
  assert.match(ui.messages[2], /delete this video/);
  metadata.submit({ currentTarget: metadata, preventDefault() {} });
  assert.equal(ui.error.textContent, 'The “Title” field is too long. Use fewer characters.');
  assert.equal(ui.translate('edit.fieldTooLong', { Field: '<b>{{.Field}}</b>' }), 'The “<b>{{.Field}}</b>” field is too long. Use fewer characters.');
  assert.equal(ui.translate('__proto__'), '__proto__');
});

test('all browser message literals refer to English source IDs', () => {
  for (const name of ['app.js', 'upload.js', 'player.js']) {
    const code = fs.readFileSync(path.join(__dirname, '../web/static', name), 'utf8');
    for (const [, id] of code.matchAll(/t\('([^']*)'/g)) assert.ok(Object.hasOwn(catalogs.en, id), `Missing ${name} message: ${id}`);
  }
});

test('upload and playback errors run in either language', () => {
  for (const lang of Object.keys(catalogs)) {
    const handlers = {}, error = { hidden: true, textContent: '' };
    const form = { elements: { file: { files: [{ name: 'invalid.txt', size: 1 }], addEventListener() {} } }, dataset: { maxBytes: '2048' }, addEventListener(name, fn) { handlers[name] = fn; } };
    const inert = { addEventListener() {} };
    const document = {
      documentElement: { lang },
      getElementById(id) { return id === 'upload-form' ? form : error; },
      querySelector() { return inert; }
    };
    const window = { tinyTubeMessages: browserMessages(lang), XMLHttpRequest: true, FormData: true };
    const context = vm.createContext({ document, window });
    vm.runInContext(i18nSource, context);
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/static/upload.js'), 'utf8'), context);
    handlers.submit({ preventDefault() {} });
    assert.equal(error.hidden, false);
    assert.equal(error.textContent, (catalogs[lang]["upload.invalidFormat"] || catalogs.en["upload.invalidFormat"]).other);

    const video = { dataset: { id: 'video', hls: '', original: 'original.mp4' }, addEventListener(name, fn) { handlers[name] = fn; }, load() {} };
    document.getElementById = id => id === 'v' ? video : error;
    document.querySelectorAll = () => [];
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/static/player.js'), 'utf8'), context);
    assert.equal(video.src, '/videos/video/original.mp4');
    handlers.error();
    assert.equal(error.textContent, (catalogs[lang]["player.unsupported"] || catalogs.en["player.unsupported"]).other);
  }
});

test('new language catalogs use CLDR plurals and English fallbacks', () => {
  function translator(lang, partial) {
    const window = { tinyTubeMessages: browserMessages(lang, partial) };
    vm.runInNewContext(i18nSource, {window, document:{documentElement:{lang}}});
    return window.tinyTubeTranslate;
  }
  const russian = translator('ru', {'count.videos':{one:'{{.Count}} one',few:'{{.Count}} few',many:'{{.Count}} many',other:'{{.Count}} other'}});
  for (const [Count, form] of [[1,'one'],[2,'few'],[5,'many'],[21,'one'],[22,'few'],[25,'many']]) assert.equal(russian('count.videos',{Count}), `${Count} ${form}`);
  assert.equal(russian('count.views',{Count:21}), '21 views');
  assert.equal(russian('share.copy'), 'Copy');
  const arabic = translator('ar', {'count.videos':{zero:'{{.Count}} zero',one:'{{.Count}} one',two:'{{.Count}} two',few:'{{.Count}} few',many:'{{.Count}} many',other:'{{.Count}} other'}});
  for(const [Count,form] of [[0,'zero'],[1,'one'],[2,'two'],[3,'few'],[11,'many'],[100,'other']]) assert.equal(arabic('count.videos',{Count}), `${new Intl.NumberFormat('ar').format(Count)} ${form}`);
});
