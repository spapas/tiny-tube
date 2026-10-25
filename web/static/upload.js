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
  let sending = false, dragDepth = 0;
  const formatSize = bytes => {
	if (bytes < 1024) return bytes.toLocaleString(document.documentElement.lang === 'en' ? 'en-GB' : 'el-GR') + ' bytes';
    const unit = bytes >= 1024 ** 3 ? 'GiB' : bytes >= 1024 ** 2 ? 'MiB' : 'KiB';
    const divisor = unit === 'GiB' ? 1024 ** 3 : unit === 'MiB' ? 1024 ** 2 : 1024;
    return (bytes / divisor).toLocaleString(document.documentElement.lang === 'en' ? 'en-GB' : 'el-GR', { maximumFractionDigits: 1 }) + ' ' + unit;
  };
  const showError = text => { error.textContent = text; error.hidden = false; };
  const validFile = file => {
    if (!/\.(mp4|mov|mkv|webm)$/i.test(file.name)) { showError(t('Επιλέξτε ένα βίντεο σε μορφή MP4, MOV, MKV ή WEBM.')); return false; }
    if (!file.size || file.size >= limit) { showError(t('Το αρχείο πρέπει να έχει περιεχόμενο και να είναι μικρότερο από %s.', formatSize(limit))); return false; }
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
      } catch (_) { showError(t('Χρησιμοποιήστε την επιλογή αρχείου για να προσθέσεις το βίντεο.')); }
    }
  }));
  form.addEventListener('submit', event => {
    if (!window.XMLHttpRequest || !window.FormData) return;
    event.preventDefault();
    if (sending) return;
    error.hidden = true;
    const file = fileInput.files[0];
    if (!file || !validFile(file)) { if (!file) showError(t('Επιλέξτε πρώτα ένα βίντεο.')); return; }
    const encoder = new TextEncoder();
    for (const [name, maximum, message] of [['title', 512, t('Ο τίτλος είναι πολύ μεγάλος.')], ['desc', 8192, t('Η περιγραφή είναι πολύ μεγάλη.')], ['category', 256, t('Η κατηγορία είναι πολύ μεγάλη.')]]) {
      const value = name === 'title' ? form.elements[name].value.trim() : form.elements[name].value;
      if (encoder.encode(value).length > maximum) { showError(message + ' ' + t('Χρησιμοποιήστε λιγότερους χαρακτήρες.')); return; }
    }
    const body = new FormData(form);
    const controls = [...form.elements];
    const previousDisabled = controls.map(control => control.disabled);
    sending = true; controls.forEach(control => { control.disabled = true; });
    form.setAttribute('aria-busy', 'true'); progress.hidden = false;
    const label = document.querySelector('[data-progress-label]');
    const percentage = document.querySelector('[data-progress-percent]');
    const track = document.querySelector('.progress-track');
    const bar = document.querySelector('[data-progress-bar]');
    label.textContent = t('Ανέβασμα βίντεο…'); percentage.textContent = '0%'; bar.style.width = '0%'; track.setAttribute('aria-valuenow', '0');
    const reset = () => { sending = false; controls.forEach((control, i) => { control.disabled = previousDisabled[i]; }); form.removeAttribute('aria-busy'); progress.hidden = true; };
    const request = new XMLHttpRequest();
    request.open('POST', form.action);
    request.upload.addEventListener('progress', event => {
      if (!event.lengthComputable) return;
      const value = Math.min(100, Math.round(event.loaded / event.total * 100));
      percentage.textContent = value + '%'; bar.style.width = value + '%'; track.setAttribute('aria-valuenow', String(value));
      if (value === 100) label.textContent = t('Αποθήκευση βίντεο…');
    });
    request.addEventListener('load', () => {
      const destination = new URL(request.responseURL || form.action, window.location.href);
      if (request.status >= 200 && request.status < 300 && destination.origin === window.location.origin && destination.pathname === '/watch') {
        window.location.assign(destination.href); return;
      }
      reset();
      const messages = { 400: t('Ελέγξτε τον τίτλο και τα στοιχεία του βίντεο.'), 401: t('Η σύνδεση έληξε. Ανανεώστε τη σελίδα και συνδεθείτε ξανά.'), 403: t('Η αποστολή δεν επιτρέπεται. Ανοίξτε ξανά τη σελίδα ανεβάσματος από τη βιβλιοθήκη.'), 408: t('Η αποστολή ξεπέρασε το διαθέσιμο χρονικό όριο. Ελέγξτε τη σύνδεσή σας και δοκιμάστε ξανά.'), 413: t('Το συνολικό μέγεθος ξεπερνά το όριο των %s.', formatSize(limit)), 415: t('Το αρχείο δεν αναγνωρίζεται ως υποστηριζόμενο βίντεο.'), 429: t('Έγιναν πολλές προσπάθειες σε μικρό διάστημα. Περιμένετε ένα λεπτό και δοκιμάστε ξανά.') };
      showError(messages[request.status] || t('Το βίντεο δεν αποθηκεύτηκε. Δοκιμάστε ξανά σε λίγο.'));
    });
    request.addEventListener('error', () => { reset(); showError(t('Η σύνδεση διακόπηκε. Ελέγξτε τη βιβλιοθήκη πριν επαναλάβετε την αποστολή.')); });
    request.addEventListener('abort', () => { reset(); showError(t('Το ανέβασμα διακόπηκε.')); });
    try { request.send(body); } catch (_) { reset(); showError(t('Δεν ήταν δυνατή η αποστολή. Δοκιμάστε ξανά.')); }
  });
})();
