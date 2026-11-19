// The admin event feed reloads every 10 seconds (VERSION-1-1.md §5.4),
// unless "pause refresh" is ticked — for reading a long line or selecting
// text. The choice is remembered for this browser tab only.
(function () {
  'use strict';
  var feed = document.getElementById('feed');
  var pause = document.getElementById('pause');
  if (!feed) return;
  try { pause.checked = sessionStorage.getItem('ctf-events-pause') === '1'; } catch (e) { /* storage blocked */ }
  pause.addEventListener('change', function () {
    try { sessionStorage.setItem('ctf-events-pause', pause.checked ? '1' : '0'); } catch (e) { /* ignore */ }
  });
  var secs = +feed.getAttribute('data-refresh') || 10;
  setInterval(function () { if (!pause.checked) location.reload(); }, secs * 1000);
})();
