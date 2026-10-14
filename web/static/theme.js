try {
  document.documentElement.dataset.theme = localStorage.getItem('tiny-tube-theme') === 'dark' ? 'dark' : 'light';
} catch (_) {
  document.documentElement.dataset.theme = 'light';
}
