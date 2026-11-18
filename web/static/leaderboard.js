// Refreshes the leaderboard (and on the beamer the "just solved" list) every
// 10 seconds from <base>/api/v1/leaderboard (DESIGN.md §8.3).
//
// Every value goes into the page through textContent, never innerHTML, so a
// name can never become markup, whatever the server let through.
(function () {
  'use strict';
  var table = document.getElementById('board');
  if (!table || !window.fetch) return;
  var api = table.getAttribute('data-api');
  var base = table.getAttribute('data-base');
  var tbody = table.querySelector('tbody');
  var recent = document.getElementById('recent');
  var updated = document.getElementById('updated');

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }
  function hm(iso) {
    if (!iso) return '–';
    var d = new Date(iso);
    return ('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2);
  }
  function stars(n) { return new Array(n + 1).join('★'); }

  function render(data) {
    var rows = data.rows || [];
    var frag = document.createDocumentFragment();
    if (rows.length === 0) {
      var tr = el('tr');
      var td = el('td', 'empty', 'No participants yet. As soon as a student signs up on their instance, they appear here.');
      td.colSpan = 5;
      tr.appendChild(td);
      frag.appendChild(tr);
    }
    rows.forEach(function (r) {
      var tr = el('tr', r.rank === 1 ? 'top1' : '');
      tr.appendChild(el('td', 'rank', String(r.rank)));
      var name = el('td');
      var dot = el('span', r.online ? 'dot on' : 'dot');
      dot.title = r.online ? 'online' : 'offline';
      var a = el('a', '', r.name);
      a.href = base + '/p/' + r.participant;
      name.appendChild(dot);
      name.appendChild(a);
      tr.appendChild(name);
      tr.appendChild(el('td', 'n', String(r.score)));
      tr.appendChild(el('td', 'n', r.solved + '/' + r.total));
      tr.appendChild(el('td', 'n hide-narrow', hm(r.last_solve)));
      frag.appendChild(tr);
    });
    tbody.replaceChildren(frag);

    if (recent) {
      var list = document.createDocumentFragment();
      (data.recent || []).forEach(function (s) {
        var li = el('li');
        li.appendChild(el('span', 'stars', stars(s.difficulty)));
        li.appendChild(document.createTextNode(' '));
        li.appendChild(el('strong', '', s.name));
        li.appendChild(document.createTextNode(' '));
        li.appendChild(el('span', 'muted', s.challenge));
        li.appendChild(document.createTextNode(' '));
        li.appendChild(el('time', '', hm(s.solved_at)));
        list.appendChild(li);
      });
      if (!list.childNodes.length) list.appendChild(el('li', 'muted', 'Nothing yet.'));
      recent.replaceChildren(list);
    }
    if (updated) updated.textContent = 'Updated ' + hm(data.generated_at) + ' · every 10 seconds';
  }

  function tick() {
    fetch(api, { cache: 'no-store', credentials: 'same-origin' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) { if (d) render(d); })
      .catch(function () { if (updated) updated.textContent = 'Not reachable, retrying…'; });
  }
  setInterval(tick, 10000);
})();
