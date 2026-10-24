// Focused behavior tests for embedded UI scripts; no packages or browser needed.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../web/static/app.js'), 'utf8');

function boot({ forms = [], metadata = null, share = null } = {}) {
  const messages = [], copied = [], error = { hidden: true, textContent: '' };
  let copyHandler;
  const document = {
    documentElement: { dataset: { theme: 'light' } },
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
  vm.runInNewContext(source, { document, window, URL, TextEncoder, navigator: { clipboard: { async writeText(value) { copied.push(value); } } } });
  return { messages, copied, error, copy: () => copyHandler() };
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
