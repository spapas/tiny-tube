(() => {
  const t = window.tinyTubeTranslate;
  const video = document.getElementById('v');
  if (!video) return;
  const { id, hls, original } = video.dataset;
  const message = document.getElementById('playback-error');
  const resume = document.getElementById('playback-resume');
  const resumeButton = resume.querySelector('[data-resume]');
  const storageKey = 'tiny-tube-position:' + id;
  const base = '/videos/' + encodeURIComponent(id) + '/';
  const speedButtons = document.querySelectorAll('[data-speed]');
  const syncSpeed = () => speedButtons.forEach(button => button.setAttribute('aria-pressed', String(Number(button.dataset.speed) === video.playbackRate)));
  speedButtons.forEach(button => button.addEventListener('click', () => { video.playbackRate = Number(button.dataset.speed); syncSpeed(); }));
  video.addEventListener('ratechange', syncSpeed);
  syncSpeed();

  let savedTime = 0;
  let resumeTarget = null;
  let started = false;
  let nextSave = 0;
  try { savedTime = Number(window.localStorage.getItem(storageKey)); } catch (_) {}

  function rememberPosition(seconds) {
    if (seconds === savedTime) return;
    try {
      if (seconds > 0) window.localStorage.setItem(storageKey, String(seconds));
      else window.localStorage.removeItem(storageKey);
      savedTime = seconds;
    } catch (_) {}
  }

  function savePosition() {
    if (!started || !video.readyState || video.ended || video.error) return;
    const seconds = Math.floor(video.currentTime);
    if (Number.isSafeInteger(seconds) && seconds >= 0 && seconds < video.duration) rememberPosition(seconds);
  }

  video.addEventListener('timeupdate', () => {
    const now = Date.now();
    if (!started || now < nextSave) return;
    nextSave = now + 5000;
    savePosition();
  });
  video.addEventListener('pause', savePosition);
  window.addEventListener('pagehide', savePosition);
  document.addEventListener('visibilitychange', () => { if (document.hidden) savePosition(); });
  video.addEventListener('ended', () => { rememberPosition(0); resumeTarget = null; });
  video.addEventListener('play', () => { started = true; resume.hidden = true; });

  const play = () => { video.play()?.catch(() => {}); };
  resumeButton.addEventListener('click', () => {
    resumeTarget = savedTime;
    video.currentTime = resumeTarget;
    video.focus({ preventScroll: true });
    play();
  });
  resume.querySelector('[data-start-over]').addEventListener('click', () => {
    resumeTarget = null;
    rememberPosition(0);
    video.currentTime = 0;
    video.focus({ preventScroll: true });
    play();
  });

  const seekToTimestamp = () => {
    const match = /^#t=([0-9]+)$/.exec(window.location.hash);
    if (!match || !video.readyState) return false;
    const seconds = Number(match[1]);
    if (!Number.isSafeInteger(seconds) || !(seconds <= video.duration)) return false;
    resumeTarget = null;
    resume.hidden = true;
    video.currentTime = seconds;
    return true;
  };
  video.addEventListener('loadedmetadata', () => {
    if (seekToTimestamp()) return;
    if (resumeTarget !== null && resumeTarget < video.duration) {
      video.currentTime = resumeTarget;
      return;
    }
    if (started || !Number.isSafeInteger(savedTime) || savedTime <= 0 || !(savedTime < video.duration)) return;
    const hours = Math.floor(savedTime / 3600);
    const minutes = String(Math.floor(savedTime / 60) % 60).padStart(2, '0');
    const seconds = String(savedTime % 60).padStart(2, '0');
    const time = (hours ? hours + ':' : '') + minutes + ':' + seconds;
    resumeButton.textContent = t('player.resume', { Time: time });
    resume.hidden = false;
  });
  window.addEventListener('hashchange', seekToTimestamp);
  document.querySelectorAll('[data-chapter]').forEach(link => link.addEventListener('click', event => {
    if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    if (window.location.hash === link.hash) seekToTimestamp();
    video.focus({ preventScroll: true });
    video.scrollIntoView({ block: 'nearest' });
  }));
  // YouTube-style start: attempt playback with sound. Browsers allow it for
  // engaged viewers (prior interaction with this site) and reject it otherwise;
  // a rejected attempt leaves the video paused for click-to-play, always with sound.
  video.addEventListener('canplay', () => { if (resume.hidden) play(); }, { once: true });
  let player = null, usingOriginal = false;
  const showMessage = text => { message.textContent = text; message.hidden = false; };
  function fallback() {
    resume.hidden = true;
    if (usingOriginal) {
      showMessage(t('player.unsupported'));
      return;
    }
    usingOriginal = true;
    if (resumeTarget !== null && video.currentTime > 0) resumeTarget = video.currentTime;
    if (player) { player.destroy(); player = null; }
    if (hls) showMessage(t('player.originalFallback'));
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
