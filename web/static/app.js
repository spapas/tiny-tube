(() => {
  const t = window.tinyTubeTranslate;
  const root = document.documentElement;
  document.querySelectorAll('.video-poster').forEach(image => {
    image.addEventListener('error', () => { image.hidden = true; });
    if (image.complete && !image.naturalWidth) image.hidden = true;
  });
  document.querySelectorAll('[data-delete-video]').forEach(form => form.addEventListener('submit', event => {
    const messages = {
      '/api/admin/users/delete': t('Να διαγραφεί οριστικά αυτός ο χρήστης και να ανακληθεί η πρόσβασή του;'),
      '/api/admin/lists/delete': t('Να διαγραφεί οριστικά αυτή η κλειδωμένη λίστα;')
    };
    const path = new URL(form.action, window.location.href).pathname;
    if (!window.confirm(messages[path] || t('Να διαγραφεί οριστικά το βίντεο και όλα τα αρχεία του;'))) event.preventDefault();
  }));
  document.querySelector('[data-metadata-form]')?.addEventListener('submit', event => {
    const form = event.currentTarget;
    const error = document.getElementById('edit-error');
    for (const [name, maximum] of [['title',512],['desc',8192],['category',256]]) {
      if (new TextEncoder().encode(form.elements[name].value.trim()).length > maximum) {
        event.preventDefault(); error.hidden = false;
        error.textContent = t('Το πεδίο «%s» είναι πολύ μεγάλο. Χρησιμοποιήστε λιγότερους χαρακτήρες.', t(({title:'Τίτλος',desc:'Περιγραφή',category:'Κατηγορία'})[name]));
        form.elements[name].focus(); return;
      }
    }
  });
  const toggle = document.querySelector('[data-theme-toggle]');
  const syncTheme = () => {
    const dark = root.dataset.theme === 'dark';
    toggle?.setAttribute('aria-pressed', String(dark));
    toggle?.setAttribute('aria-label', dark ? t('Ενεργοποίηση ανοιχτού θέματος') : t('Ενεργοποίηση σκοτεινού θέματος'));
    if (toggle) toggle.title = toggle.getAttribute('aria-label');
    const label = document.querySelector('[data-theme-label]');
    if (label) label.textContent = dark ? t('Σκούρο') : t('Ανοιχτό');
  };
  syncTheme();
  toggle?.addEventListener('click', () => {
    root.dataset.theme = root.dataset.theme === 'dark' ? 'light' : 'dark';
    try { localStorage.setItem('tiny-tube-theme', root.dataset.theme); } catch (_) {}
    syncTheme();
  });
  window.addEventListener('storage', event => {
    if (event.key !== 'tiny-tube-theme') return;
    root.dataset.theme = event.newValue === 'dark' ? 'dark' : 'light';
    syncTheme();
  });

  const shareInput = document.querySelector('[data-share-url]');
  if (shareInput) {
    const sharedURL = new URL(shareInput.dataset.sharePath || window.location.href, window.location.origin);
    sharedURL.username = ''; sharedURL.password = '';
    shareInput.value = sharedURL.href;
  }
  document.querySelector('[data-copy-link]')?.addEventListener('click', async () => {
    const status = document.querySelector('[data-share-status]');
    try {
      await navigator.clipboard.writeText(shareInput.value);
      status.textContent = t('Ο σύνδεσμος αντιγράφηκε.');
    } catch (_) {
      shareInput.focus();
      shareInput.select();
      status.textContent = t('Ο σύνδεσμος επιλέχθηκε. Αντιγράψτε τον από το πεδίο.');
    }
  });
})();
