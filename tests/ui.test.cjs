// Focused behavior tests for embedded UI scripts; no packages or browser needed.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../web/static/app.js'), 'utf8');
const i18nSource = fs.readFileSync(path.join(__dirname, '../web/static/i18n.js'), 'utf8');
const playerSource = fs.readFileSync(path.join(__dirname, '../web/static/player.js'), 'utf8');
const localePath = path.join(__dirname, '../web/locales');
const catalogs = Object.fromEntries(fs.readdirSync(localePath).filter(name => name.endsWith('.json')).map(name => [name.slice(0,-5), JSON.parse(fs.readFileSync(path.join(localePath, name), 'utf8'))]));

function browserMessages(lang, partial = catalogs[lang] || {}) {
  return Object.fromEntries(Object.entries({ ...catalogs.en, ...partial }).map(([id, message]) => [id, { ...message, language: Object.hasOwn(partial, id) ? lang : 'en' }]));
}

function boot({ forms = [], metadata = null, share = null, video = null, lang = 'el' } = {}) {
  const messages = [], copied = [], error = { hidden: true, textContent: '' };
  let copyHandler, copyTimestampHandler;
  const document = {
    documentElement: { lang, dataset: { theme: 'light' } },
    querySelectorAll(selector) { return selector === '[data-delete-video]' ? forms : []; },
    querySelector(selector) {
      if (selector === '[data-metadata-form]') return metadata;
      if (selector === '[data-share-url]') return share;
      if (selector === '[data-copy-link]' && share) return { addEventListener(_, fn) { copyHandler = fn; } };
      if (selector === '[data-copy-timestamp]' && video) return { addEventListener(_, fn) { copyTimestampHandler = fn; } };
      if (selector === '[data-share-status]') return { textContent: '' };
      return null;
    },
    getElementById(id) { return id === 'v' ? video : error; }
  };
  const window = { location: { href: 'http://review.test/watch?id=video', origin: 'http://review.test' }, addEventListener() {}, confirm(message) { messages.push(message); return false; } };
  window.tinyTubeMessages = browserMessages(lang);
  const context = vm.createContext({ document, window, URL, TextEncoder, navigator: { clipboard: { async writeText(value) { copied.push(value); } } } });
  vm.runInContext(i18nSource, context);
  vm.runInContext(source, context);
  return { messages, copied, error, translate: window.tinyTubeTranslate, copy: () => copyHandler(), copyTimestamp: () => copyTimestampHandler() };
}

test('user, list, video and subtitle deletion receive the correct confirmation', () => {
  const forms = ['/api/admin/users/delete', '/api/admin/lists/delete', '/api/delete', '/api/subtitles/delete'].map(action => ({ action, addEventListener(_, fn) { this.submit = fn; } }));
  const ui = boot({ forms });
  for (const form of forms) {
    let prevented = false;
    form.submit({ preventDefault() { prevented = true; } });
    assert.equal(prevented, true);
  }
  assert.match(ui.messages[0], /χρήστης/);
  assert.match(ui.messages[1], /λίστα/);
  assert.match(ui.messages[2], /βίντεο/);
  assert.match(ui.messages[3], /υπότιτλοι/);
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

test('timestamp sharing keeps the locked key and leaves the plain share action available', async () => {
  const share = { dataset: { sharePath: '/watch?id=video&key=lt-capability' } };
  const ui = boot({ share, video: { currentTime: 1395.8 } });
  await ui.copyTimestamp();
  assert.equal(share.value, 'http://review.test/watch?id=video&key=lt-capability#t=1395');
  await ui.copy();
  assert.deepEqual(ui.copied, [
    'http://review.test/watch?id=video&key=lt-capability#t=1395',
    'http://review.test/watch?id=video&key=lt-capability'
  ]);
});

function bootPlayer({ hash = '', storage = new Map() } = {}) {
  const handlers = {};
  const chapter = { hash: '#t=45', addEventListener(_, fn) { handlers.chapter = fn; } };
  const resumeButton = { addEventListener(_, fn) { handlers.resume = fn; } };
  const startOver = { addEventListener(_, fn) { handlers.startOver = fn; } };
  const resume = { hidden: true, querySelector(selector) { return selector === '[data-resume]' ? resumeButton : startOver; } };
  const video = {
    dataset: { id: 'video', hls: 'index.m3u8', original: 'original.mp4' },
    currentTime: 0, duration: 60, readyState: 0, plays: 0,
    addEventListener(name, fn) { handlers[name] = fn; },
    canPlayType() { return 'maybe'; },
    load() { this.readyState = 0; this.currentTime = 0; },
    play() { this.plays++; handlers.play(); return Promise.resolve(); },
    focus() {},
    scrollIntoView() {}
  };
  const document = {
    getElementById(id) { return { v: video, 'playback-resume': resume }[id] || {}; },
    addEventListener(name, fn) { handlers[name] = fn; },
    querySelectorAll(selector) { return selector === '[data-chapter]' ? [chapter] : []; }
  };
  const window = {
    location: { hash },
    addEventListener(name, fn) { handlers[name] = fn; },
    get localStorage() {
      if (!storage) throw new Error('Storage blocked');
      return { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) };
    },
    tinyTubeTranslate(id, values) { return catalogs.en[id].other.replace('{{.Time}}', values?.Time); }
  };
  vm.runInNewContext(playerSource, { document, window });
  return { handlers, chapter, video, document, window, resume, resumeButton };
}

const positionKey = 'tiny-tube-position:video';

test('timestamp links override saved progress and survive HLS fallback', () => {
  const { handlers, chapter, video, window, resume } = bootPlayer({ hash: '#t=20', storage: new Map([[positionKey, '30']]) });
  assert.equal(video.src, '/videos/video/index.m3u8');
  assert.equal(video.currentTime, 0);
  video.readyState = 1;
  handlers.loadedmetadata();
  assert.equal(video.currentTime, 20);
  assert.equal(resume.hidden, true);
  handlers.canplay();
  assert.equal(video.plays, 1);

  window.location.hash = chapter.hash;
  handlers.hashchange();
  assert.equal(video.currentTime, 45);
  video.currentTime = 51;
  handlers.chapter({ button: 0, ctrlKey: true });
  assert.equal(video.currentTime, 51);
  handlers.chapter({ button: 0 });
  assert.equal(video.currentTime, 45);

  handlers.error();
  assert.equal(video.src, '/videos/video/original.mp4');
  video.readyState = 1;
  handlers.loadedmetadata();
  assert.equal(video.currentTime, 45);

  for (const hash of ['#t=1e1', '#t=-1', '#t=61', '#t=99999999999999999999']) {
    window.location.hash = hash;
    handlers.hashchange();
    assert.equal(video.currentTime, 45);
  }
  window.location.hash = '#t=0';
  handlers.hashchange();
  assert.equal(video.currentTime, 0);
});

test('unfinished playback can be resumed after revisiting and clears on completion', () => {
  const storage = new Map();
  const first = bootPlayer({ storage });
  first.video.readyState = 1;
  first.handlers.loadedmetadata();
  first.handlers.canplay();
  first.video.currentTime = 15.9;
  first.handlers.timeupdate();
  assert.equal(storage.get(positionKey), '15');
  first.video.currentTime = 24.2;
  first.handlers.pagehide();
  assert.equal(storage.get(positionKey), '24');

  const { handlers, video, resume, resumeButton } = bootPlayer({ storage });
  video.readyState = 1;
  handlers.loadedmetadata();
  handlers.canplay();
  assert.equal(video.plays, 0);
  assert.equal(resume.hidden, false);
  assert.equal(resumeButton.textContent, 'Resume from 00:24');
  handlers.pagehide();
  assert.equal(storage.get(positionKey), '24');
  handlers.resume();
  assert.equal(video.currentTime, 24);
  assert.equal(video.plays, 1);
  assert.equal(resume.hidden, true);

  video.currentTime = 25;
  handlers.error();
  video.readyState = 1;
  handlers.loadedmetadata();
  assert.equal(video.currentTime, 25);
  video.currentTime = video.duration;
  video.ended = true;
  handlers.ended();
  handlers.pagehide();
  assert.equal(storage.has(positionKey), false);
});

test('start over clears saved progress and blocked storage preserves autoplay', () => {
  const storage = new Map([[positionKey, '20']]);
  const { handlers, video, resume } = bootPlayer({ storage });
  video.readyState = 1;
  handlers.loadedmetadata();
  handlers.startOver();
  assert.equal(resume.hidden, true);
  assert.equal(video.currentTime, 0);
  assert.equal(video.plays, 1);
  assert.equal(storage.has(positionKey), false);

  for (const unavailable of [null, new Map([[positionKey, '60']]), new Map([[positionKey, 'invalid']])]) {
    const fresh = bootPlayer({ storage: unavailable });
    fresh.video.readyState = 1;
    fresh.handlers.loadedmetadata();
    fresh.handlers.canplay();
    assert.equal(fresh.resume.hidden, true);
    assert.equal(fresh.video.plays, 1);
    fresh.video.currentTime = 1;
    fresh.handlers.pause();
  }
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
    const window = { tinyTubeMessages: browserMessages(lang), XMLHttpRequest: true, FormData: true, addEventListener() {} };
    const context = vm.createContext({ document, window });
    vm.runInContext(i18nSource, context);
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/static/upload.js'), 'utf8'), context);
    handlers.submit({ preventDefault() {} });
    assert.equal(error.hidden, false);
    assert.equal(error.textContent, (catalogs[lang]["upload.invalidFormat"] || catalogs.en["upload.invalidFormat"]).other);

    const video = { dataset: { id: 'video', hls: '', original: 'original.mp4' }, addEventListener(name, fn) { handlers[name] = fn; }, load() {} };
    document.getElementById = id => ({ v: video, 'playback-resume': { querySelector: () => inert } }[id] || error);
    document.querySelectorAll = () => [];
    document.addEventListener = () => {};
    vm.runInContext(playerSource, context);
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
