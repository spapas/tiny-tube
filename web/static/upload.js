(() => {
  const t = window.tinyTubeTranslate;
  const form = document.getElementById('upload-form');
  if (!form) return;
  const fileInput = form.elements.file;
  const dropZone = document.querySelector('[data-drop-zone]');
  const summary = document.getElementById('file-summary');
  const error = document.getElementById('upload-error');
  const progress = document.querySelector('.upload-progress');
  const limit = Number(form.dataset.maxBytes);
  const fallbackChunkSize = 8 * 1024 * 1024;
  const maxChunkAttempts = 3;
  let sending = false, dragDepth = 0;
  const storage = (() => { try { return window.localStorage || null; } catch (_) { return null; } })();
  const formatSize = bytes => {
    if (bytes < 1024) return t('size.bytes', { Count: bytes });
    const unit = bytes >= 1024 ** 3 ? 'GiB' : bytes >= 1024 ** 2 ? 'MiB' : 'KiB';
    const divisor = unit === 'GiB' ? 1024 ** 3 : unit === 'MiB' ? 1024 ** 2 : 1024;
    return (bytes / divisor).toLocaleString(document.documentElement.lang, { maximumFractionDigits: 1 }) + ' ' + unit;
  };
  const showError = text => { error.textContent = text; error.hidden = false; };
  const validFile = file => {
    if (!/\.(mp4|mov|mkv|webm)$/i.test(file.name)) { showError(t('upload.invalidFormat')); return false; }
    if (!file.size || file.size >= limit) { showError(t('upload.invalidSize', { Limit: formatSize(limit) })); return false; }
    return true;
  };
  function updateFile() {
    error.hidden = true;
    const file = fileInput.files[0];
    summary.hidden = !file;
    if (!file) return;
    document.querySelector('[data-file-name]').textContent = file.name;
    document.querySelector('[data-file-size]').textContent = formatSize(file.size);
    validFile(file);
  }
  fileInput.addEventListener('change', updateFile);
  document.querySelector('[data-clear-file]').addEventListener('click', () => { fileInput.value = ''; updateFile(); fileInput.focus(); });
  ['dragenter', 'dragover', 'dragleave', 'drop'].forEach(name => dropZone.addEventListener(name, event => {
    event.preventDefault();
    if (sending) return;
    if (name === 'dragenter') { dragDepth++; dropZone.classList.add('is-dragging'); }
    if (name === 'dragleave') { dragDepth--; if (dragDepth <= 0) dropZone.classList.remove('is-dragging'); }
    if (name === 'drop') {
      dragDepth = 0; dropZone.classList.remove('is-dragging');
      const file = event.dataTransfer.files[0];
      if (!file) return;
      try {
        const transfer = new DataTransfer(); transfer.items.add(file); fileInput.files = transfer.files;
        updateFile();
      } catch (_) { showError(t('upload.pickerError')); }
    }
  }));
  const resumeKey = file => 'tube-upload:' + file.name + ':' + file.size + ':' + file.lastModified;
  const remember = (file, id) => {
    if (!storage) return;
    try { storage.setItem(resumeKey(file), JSON.stringify({ id, size: file.size })); } catch (_) {}
  };
  const forget = file => {
    if (!storage) return;
    try { storage.removeItem(resumeKey(file)); } catch (_) {}
  };
  const recalled = file => {
    if (!storage) return null;
    try {
      const saved = JSON.parse(storage.getItem(resumeKey(file)));
      if (saved && saved.id && saved.size === file.size) return saved;
    } catch (_) {}
    return null;
  };
  const postJSON = (url, payload) => new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    request.open('POST', url);
    request.setRequestHeader('Content-Type', 'application/json');
    request.setRequestHeader('Accept', 'application/json');
    request.addEventListener('load', () => {
      let data = null;
      try { data = request.responseText ? JSON.parse(request.responseText) : null; } catch (_) {}
      if (request.status >= 200 && request.status < 300) resolve({ status: request.status, data });
      else reject({ status: request.status, data });
    });
    request.addEventListener('error', () => reject({ status: 0 }));
    request.addEventListener('timeout', () => reject({ status: 0 }));
    try { request.send(JSON.stringify(payload)); } catch (_) { reject({ status: 0 }); }
  });
  const getJSON = url => new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    request.open('GET', url);
    request.setRequestHeader('Accept', 'application/json');
    request.addEventListener('load', () => {
      let data = null;
      try { data = request.responseText ? JSON.parse(request.responseText) : null; } catch (_) {}
      if (request.status >= 200 && request.status < 300) resolve(data);
      else reject({ status: request.status });
    });
    request.addEventListener('error', () => reject({ status: 0 }));
    try { request.send(); } catch (_) { reject({ status: 0 }); }
  });
  const sendChunk = (id, offset, blob, total, onProgress) => new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    request.open('POST', '/api/uploads/' + id + '/chunk?offset=' + offset);
    request.upload.addEventListener('progress', event => {
      if (!event.lengthComputable) return;
      onProgress(offset + event.loaded, total);
    });
    request.addEventListener('load', () => {
      let data = null;
      try { data = request.responseText ? JSON.parse(request.responseText) : null; } catch (_) {}
      if (request.status >= 200 && request.status < 300 && data && typeof data.received === 'number') resolve(data.received);
      else if (request.status === 409 && data && typeof data.received === 'number') reject({ resumed: data.received });
      else reject({ status: request.status });
    });
    request.addEventListener('error', () => reject({ status: 0 }));
    request.addEventListener('timeout', () => reject({ status: 408 }));
    try { request.send(blob); } catch (_) { reject({ status: 0 }); }
  });
  form.addEventListener('submit', event => {
    if (!window.XMLHttpRequest || !window.FormData) return;
    event.preventDefault();
    if (sending) return;
    error.hidden = true;
    const file = fileInput.files[0];
    if (!file || !validFile(file)) { if (!file) showError(t('upload.selectFirst')); return; }
    const encoder = new TextEncoder();
    for (const [name, maximum, message] of [['title', 512, t('upload.titleTooLong')], ['desc', 8192, t('upload.descriptionTooLong')], ['category', 256, t('upload.categoryTooLong')]]) {
      const value = name === 'title' ? form.elements[name].value.trim() : form.elements[name].value;
      if (encoder.encode(value).length > maximum) { showError(message + ' ' + t('validation.fewerCharacters')); return; }
    }
    const controls = [...form.elements];
    const previousDisabled = controls.map(control => control.disabled);
    sending = true; controls.forEach(control => { control.disabled = true; });
    form.setAttribute('aria-busy', 'true'); progress.hidden = false;
    const label = document.querySelector('[data-progress-label]');
    const percentage = document.querySelector('[data-progress-percent]');
    const track = document.querySelector('.progress-track');
    const bar = document.querySelector('[data-progress-bar]');
    const report = (loaded, total) => {
      const value = Math.min(100, Math.round(loaded / total * 100));
      percentage.textContent = value + '%'; bar.style.width = value + '%'; track.setAttribute('aria-valuenow', String(value));
      if (value === 100) label.textContent = t('upload.saving');
    };
    label.textContent = t('upload.uploading'); percentage.textContent = '0%'; bar.style.width = '0%'; track.setAttribute('aria-valuenow', '0');
    const reset = () => { sending = false; controls.forEach((control, i) => { control.disabled = previousDisabled[i]; }); form.removeAttribute('aria-busy'); progress.hidden = true; };
    const fail = status => {
      reset();
      const messages = { 400: t('upload.checkDetails'), 401: t('auth.sessionExpired'), 403: t('upload.forbidden'), 408: t('upload.timeout'), 413: t('upload.tooLarge', { Limit: formatSize(limit) }), 415: t('upload.unsupportedVideo'), 429: t('auth.tooManyAttempts') };
      showError(messages[status] || t('upload.notSaved'));
    };
    const finish = () => {
      const fields = new FormData();
      fields.append('title', form.elements.title.value.trim());
      fields.append('desc', form.elements.desc.value);
      fields.append('category', form.elements.category.value);
      fields.append('access_list', form.elements.access_list.value);
      const request = new XMLHttpRequest();
      request.open('POST', '/api/uploads/' + session.id + '/complete');
      request.addEventListener('load', () => {
        const destination = new URL(request.responseURL || form.action, window.location.href);
        if (request.status >= 200 && request.status < 300 && destination.origin === window.location.origin && destination.pathname === '/watch') {
          forget(file);
          window.location.assign(destination.href); return;
        }
        fail(request.status);
      });
      request.addEventListener('error', () => fail(0));
      request.addEventListener('timeout', () => fail(408));
      try { request.send(fields); } catch (_) { fail(0); }
    };
    const session = {};
    const run = async () => {
      let chunkSize = fallbackChunkSize, offset = 0;
      const saved = recalled(file);
      if (saved) {
        try {
          const state = await getJSON('/api/uploads/' + saved.id);
          if (state && state.size === file.size && typeof state.received === 'number') {
            session.id = saved.id;
            chunkSize = state.chunk_size || fallbackChunkSize;
            offset = state.received;
            if (offset > 0) label.textContent = t('upload.resuming');
          }
        } catch (_) {}
      }
      if (!session.id) {
        const created = await postJSON('/api/uploads', { filename: file.name, size: file.size });
        session.id = created.data.id;
        chunkSize = created.data.chunk_size || fallbackChunkSize;
        remember(file, session.id);
      }
      let attempts = 0;
      while (offset < file.size) {
        const end = Math.min(offset + chunkSize, file.size);
        try {
          offset = await sendChunk(session.id, offset, file.slice(offset, end), file.size, report);
          attempts = 0;
        } catch (err) {
          if (err && typeof err.resumed === 'number') { offset = err.resumed; attempts = 0; continue; }
          if (++attempts > maxChunkAttempts) throw err;
        }
      }
      finish();
    };
    run().catch(err => fail(err && err.status));
  });
})();
