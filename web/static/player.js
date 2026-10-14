(() => {
  const video = document.getElementById('v');
  if (!video) return;
  const { id, hls, original } = video.dataset;
  const message = document.getElementById('playback-error');
  const base = '/videos/' + encodeURIComponent(id) + '/';
  let player = null, usingOriginal = false;
  const showMessage = text => { message.textContent = text; message.hidden = false; };
  function fallback() {
    if (usingOriginal) {
      showMessage('Ο browser δεν μπορεί να αναπαράγει αυτό το αρχείο. Δοκίμασε άλλο browser ή κατέβασε το αρχικό βίντεο.');
      return;
    }
    usingOriginal = true;
    if (player) { player.destroy(); player = null; }
    if (hls) showMessage('Η προεπισκόπηση δεν είναι διαθέσιμη. Δοκιμάζουμε το αρχικό βίντεο.');
    video.src = base + original;
    video.load();
  }
  video.addEventListener('error', fallback);
  if (!hls) { fallback(); return; }
  if (video.canPlayType('application/vnd.apple.mpegurl')) { video.src = base + hls; return; }
  if (window.Hls && window.Hls.isSupported()) {
    const h = new window.Hls();
    player = h;
    h.on(window.Hls.Events.ERROR, (_, data) => { if (data.fatal) fallback(); });
    h.loadSource(base + hls);
    h.attachMedia(video);
  } else { fallback(); }
})();
