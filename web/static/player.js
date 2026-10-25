(() => {
  const t = window.tinyTubeTranslate;
  const video = document.getElementById('v');
  if (!video) return;
  const { id, hls, original } = video.dataset;
  const message = document.getElementById('playback-error');
  const base = '/videos/' + encodeURIComponent(id) + '/';
  const speedButtons = document.querySelectorAll('[data-speed]');
  const syncSpeed = () => speedButtons.forEach(button => button.setAttribute('aria-pressed', String(Number(button.dataset.speed) === video.playbackRate)));
  speedButtons.forEach(button => button.addEventListener('click', () => { video.playbackRate = Number(button.dataset.speed); syncSpeed(); }));
  video.addEventListener('ratechange', syncSpeed);
  syncSpeed();
  // YouTube-style start: attempt playback with sound. Browsers allow it for
  // engaged viewers (prior interaction with this site) and reject it otherwise;
  // a rejected attempt leaves the video paused for click-to-play, always with sound.
  video.addEventListener('canplay', () => { video.play()?.catch(() => {}); }, { once: true });
  let player = null, usingOriginal = false;
  const showMessage = text => { message.textContent = text; message.hidden = false; };
  function fallback() {
    if (usingOriginal) {
      showMessage(t('Ο browser δεν μπορεί να αναπαράγει αυτό το αρχείο. Δοκίμασε άλλο browser ή κατέβασε το αρχικό βίντεο.'));
      return;
    }
    usingOriginal = true;
    if (player) { player.destroy(); player = null; }
    if (hls) showMessage(t('Η προεπισκόπηση δεν είναι διαθέσιμη. Δοκιμάζουμε το αρχικό βίντεο.'));
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
