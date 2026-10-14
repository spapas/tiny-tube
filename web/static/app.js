(() => {
  const root = document.documentElement;
  document.querySelectorAll('.video-poster').forEach(image => {
    image.addEventListener('error', () => { image.hidden = true; });
    if (image.complete && !image.naturalWidth) image.hidden = true;
  });
  document.querySelector('[data-delete-video]')?.addEventListener('submit', event => {
    if (!window.confirm('Να διαγραφεί οριστικά το βίντεο και όλα τα αρχεία του;')) event.preventDefault();
  });
  const toggle = document.querySelector('[data-theme-toggle]');
  const syncTheme = () => {
    const dark = root.dataset.theme === 'dark';
    toggle?.setAttribute('aria-pressed', String(dark));
    toggle?.setAttribute('aria-label', dark ? 'Ενεργοποίηση ανοιχτού θέματος' : 'Ενεργοποίηση σκοτεινού θέματος');
    if (toggle) toggle.title = toggle.getAttribute('aria-label');
    const label = document.querySelector('[data-theme-label]');
    if (label) label.textContent = dark ? 'Σκούρο' : 'Ανοιχτό';
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
    const sharedURL = new URL(window.location.href);
    sharedURL.username = ''; sharedURL.password = '';
    shareInput.value = sharedURL.href;
  }
  document.querySelector('[data-copy-link]')?.addEventListener('click', async () => {
    const status = document.querySelector('[data-share-status]');
    try {
      await navigator.clipboard.writeText(shareInput.value);
      status.textContent = 'Ο σύνδεσμος αντιγράφηκε.';
    } catch (_) {
      shareInput.focus();
      shareInput.select();
      status.textContent = 'Ο σύνδεσμος επιλέχθηκε. Αντιγράψτε τον από το πεδίο.';
    }
  });
})();
