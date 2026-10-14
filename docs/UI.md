# Interface update — 2026-10-14

Library, watch, and upload pages share a light-first design for workplace training,
presentations, and recorded meetings: neutral white/gray surfaces, restrained navy
accents, common navigation, and a responsive card layout. Headings and spacing are
compact so the library content takes priority over decorative presentation.
Dark mode is opt-in and persists under `tiny-tube-theme` in browser-local storage.

The library combines category filters with text search and has separate empty
library/no-results states. Covers are neutral placeholders, not extracted video
frames. The initial storytelling slogans, colorful covers, large decorative numbers,
and hero illustration have been removed. Copy and instructions use a professional,
task-oriented tone throughout the three pages.
The watch page includes video information, preparation/failure notices, link
copying, and original-file access. The uploader supports drag-and-drop, selected
file details, progress, client-side validation, and recoverable inline errors.
Server-side validation and Basic Auth remain authoritative.

Templates and web assets are embedded through `ui.go`; the project still has no
frontend build dependencies. Rebuild/restart after changing `web/`.

## Browser verification

A local preview used temporary sample data and storage, without changing the
tracked playback fixture or real video directory.

- All three pages were checked at viewport widths 320, 375, 768, and 1440 pixels.
  Document width never exceeded client width, including the long-title watch page.
- The library uses one/two/three columns as screen space allows. Watch/upload side
  panels stack on smaller screens; navigation and filters wrap instead of overflow.
- Light mode was the initial theme. Dark mode survived reload and navigation;
  switching back restored light mode. System dark preference does not override it.
- Category selection and search were combined in the browser and returned the
  expected single result while retaining the active category.
- Link copying showed success. The expanded share panel also fit a 320-pixel
  viewport. Shared URLs strip any username/password components.
- The file chooser selected the tracked small video. The new uploader displayed
  its name/size, showed progress, submitted it to temporary storage, and navigated
  to its watch page. The video reached browser ready state 4 with a visible frame.
- An invalid media file returned an inline error, restored enabled controls, hid
  progress, and preserved the entered title for correction and retry.
- No JavaScript errors were reported in the inspected browser log.
- Go regression tests cover combined filters, embedded asset serving, and escaped
  watch metadata/player attributes, alongside the existing upload/FFmpeg tests.

These are local Chromium-based browser checks, not a real-device or Safari/Firefox
compatibility matrix. Native picker selection was tested; desktop OS file dragging
was not separately exercised. Without JavaScript the server-rendered library and
standard multipart upload remain available, but theme switching/progress need JS.
