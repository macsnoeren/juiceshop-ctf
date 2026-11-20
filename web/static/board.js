// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// The board, live: every 10 seconds from <game>/board.json.
//
// The page is complete without this script; it only keeps it current. It
// rebuilds the podium, the rows and the ticker, slides rows to their new
// place when ranks change (FLIP: measure, re-render, animate the difference),
// flashes a row that just scored, reloads the chart, and on the beamer
// counts down to the end and switches to the final result when the game
// closes.
//
// Every value goes into the page through textContent or setAttribute, never
// innerHTML, so a name can never become markup, whatever got through.
// With prefers-reduced-motion nothing slides or flashes (style.css too).
(function () {
  'use strict';
  var root = document.getElementById('board');
  if (!root || !window.fetch) return;
  var api = root.getAttribute('data-json');
  var base = root.getAttribute('data-base');
  var chartURL = root.getAttribute('data-chart');
  var beamer = root.hasAttribute('data-beamer');
  var wasFinal = root.hasAttribute('data-final');
  var calm = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var SVG = 'http://www.w3.org/2000/svg';
  var lastScore = {};
  var lastTicker = '';
  var updated = document.getElementById('updated');

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }
  function link(href, text, cls) { var a = el('a', cls, text); a.setAttribute('href', href); return a; }
  function hm(iso) {
    if (!iso) return '–';
    var d = new Date(iso);
    return ('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2);
  }
  // Difficulty as the templates draw it (layout.html "diff"): a blue star
  // with the number in it.
  function diff(n) {
    var d = el('span', 'diff', String(n));
    d.title = 'difficulty ' + n + ' of 6';
    d.setAttribute('aria-label', 'difficulty ' + n);
    return d;
  }

  document.querySelectorAll('#rows tr[data-pid]').forEach(function (tr) {
    lastScore[tr.getAttribute('data-pid')] = +tr.getAttribute('data-score');
  });

  function catbar(counts, cats, total, solved) {
    var svg = document.createElementNS(SVG, 'svg');
    svg.setAttribute('class', 'catbar');
    svg.setAttribute('viewBox', '0 0 ' + Math.max(total, 1) + ' 10');
    svg.setAttribute('preserveAspectRatio', 'none');
    svg.setAttribute('role', 'img');
    svg.setAttribute('aria-label', solved + ' of ' + total + ' by category');
    var track = document.createElementNS(SVG, 'rect');
    track.setAttribute('class', 'track');
    track.setAttribute('width', Math.max(total, 1));
    track.setAttribute('height', 10);
    svg.appendChild(track);
    var x = 0;
    counts.forEach(function (n, i) {
      if (!n) return;
      var r = document.createElementNS(SVG, 'rect');
      r.setAttribute('class', 'cat-' + (cats[i].index % 16));
      r.setAttribute('x', x);
      r.setAttribute('width', n);
      r.setAttribute('height', 10);
      var t = document.createElementNS(SVG, 'title');
      t.textContent = cats[i].name + ': ' + n + ' of ' + cats[i].total;
      r.appendChild(t);
      svg.appendChild(r);
      x += n;
    });
    return svg;
  }

  function podium(d) {
    var box = document.getElementById('podium');
    if (!box) return;
    var frag = document.createDocumentFragment();
    if (!d.podium.length) frag.appendChild(el('p', 'muted empty', 'The podium fills as soon as someone scores.'));
    d.podium.forEach(function (r) {
      var a = link(base + 'p/' + r.participant, undefined, 'step place-' + r.rank);
      a.appendChild(el('span', 'medal', String(r.rank)));
      a.appendChild(el('span', 'who', r.name));
      a.appendChild(el('span', 'pts', String(r.score)));
      frag.appendChild(a);
    });
    box.replaceChildren(frag);
  }

  function rows(d) {
    var tbody = document.getElementById('rows');
    if (!tbody) return;
    var before = {};
    tbody.querySelectorAll('tr[data-pid]').forEach(function (tr) {
      before[tr.getAttribute('data-pid')] = tr.getBoundingClientRect().top;
    });
    var frag = document.createDocumentFragment();
    if (!d.rows.length) {
      var tr0 = el('tr');
      var td0 = el('td', 'empty', 'Nobody has joined yet. As soon as a student joins from their VM, they appear here.');
      td0.colSpan = 5;
      tr0.appendChild(td0);
      frag.appendChild(tr0);
    }
    var scored = [];
    d.rows.forEach(function (r) {
      var tr = el('tr', r.rank <= 3 ? 'top' + r.rank : '');
      tr.setAttribute('data-pid', r.participant);
      tr.setAttribute('data-score', r.score);
      tr.appendChild(el('td', 'rank', String(r.rank)));
      var name = el('td', 'name');
      var dot = el('span', r.online ? 'dot on' : 'dot');
      dot.title = r.online ? 'online' : 'offline';
      name.appendChild(dot);
      name.appendChild(link(base + 'p/' + r.participant, r.name, 'plain'));
      if (r.first_bloods) {
        name.appendChild(document.createTextNode(' '));
        var b = el('span', 'blood', '✦' + r.first_bloods);
        b.title = 'first to solve ' + r.first_bloods + ' challenge(s)';
        name.appendChild(b);
      }
      tr.appendChild(name);
      tr.appendChild(el('td', 'n pts', String(r.score)));
      tr.appendChild(el('td', 'n' + (beamer ? '' : ' hide-narrow'), r.solved + '/' + d.total));
      var bar = el('td', 'bar' + (beamer ? '' : ' hide-narrow'));
      bar.appendChild(catbar(r.categories, d.categories, d.total, r.solved));
      tr.appendChild(bar);
      var id = String(r.participant);
      if (id in lastScore && lastScore[id] < r.score) scored.push(tr);
      lastScore[id] = r.score;
      frag.appendChild(tr);
    });
    tbody.replaceChildren(frag);
    if (calm) return;
    tbody.querySelectorAll('tr[data-pid]').forEach(function (tr) {
      var was = before[tr.getAttribute('data-pid')];
      if (was === undefined) return;
      var dy = was - tr.getBoundingClientRect().top;
      if (!dy) return;
      tr.style.transform = 'translateY(' + dy + 'px)';
      tr.style.transition = 'none';
      requestAnimationFrame(function () {
        requestAnimationFrame(function () {
          tr.style.transition = 'transform .7s ease';
          tr.style.transform = '';
        });
      });
    });
    scored.forEach(function (tr) {
      tr.classList.add('flash');
      setTimeout(function () { tr.classList.remove('flash'); }, 60);
    });
  }

  function ticker(d) {
    var list = document.getElementById('ticker');
    if (!list) return;
    var key = d.ticker.map(function (t) { return t.participant + t.challenge; }).join(',');
    if (key === lastTicker) return;
    var seen = {};
    list.querySelectorAll('li[data-k]').forEach(function (li) { seen[li.getAttribute('data-k')] = true; });
    var first = lastTicker === '';
    lastTicker = key;
    var frag = document.createDocumentFragment();
    if (!d.ticker.length) frag.appendChild(el('li', 'muted', 'Nothing solved yet.'));
    d.ticker.forEach(function (t) {
      var k = t.participant + ':' + t.challenge;
      var cls = (t.first_blood ? 'first' : '') + (!first && !seen[k] && !calm ? ' new' : '');
      var li = el('li', cls.trim());
      li.setAttribute('data-k', k);
      li.appendChild(el('time', '', hm(t.at)));
      li.appendChild(link(base + 'p/' + t.participant, t.name, 'plain strong'));
      li.appendChild(link(base + 'c/' + t.challenge, t.challenge_name, 'plain muted'));
      li.appendChild(diff(t.difficulty));
      if (t.first_blood) li.appendChild(el('span', 'badge blood', 'first blood'));
      frag.appendChild(li);
    });
    list.replaceChildren(frag);
  }

  function render(d) {
    if (beamer && d.game.state === 'closed' && !wasFinal) {
      location.reload(); // the final-result view
      return;
    }
    podium(d);
    rows(d);
    ticker(d);
    var chart = document.getElementById('chart');
    if (chart && chartURL) chart.setAttribute('src', chartURL + '?t=' + Date.now());
    if (updated) updated.textContent = 'Updated ' + hm(d.generated_at) + ' · every 10 seconds';
  }

  function tick() {
    fetch(api, { cache: 'no-store', credentials: 'same-origin' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) { if (d) render(d); })
      .catch(function () { if (updated) updated.textContent = 'Not reachable, retrying…'; });
  }
  setInterval(tick, 10000);

  // The beamer's countdown, once a second, from the end time in the page.
  var cd = document.getElementById('countdown');
  if (cd && cd.getAttribute('data-ends-at')) {
    var end = Date.parse(cd.getAttribute('data-ends-at'));
    var two = function (n) { return ('0' + n).slice(-2); };
    setInterval(function () {
      var s = Math.max(0, Math.floor((end - Date.now()) / 1000));
      var h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60;
      cd.textContent = (h ? h + ':' + two(m) : String(m)) + ':' + two(s % 60);
      if (s === 0) tick();
    }, 1000);
  }
})();
