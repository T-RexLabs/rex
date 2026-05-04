// sort.js — client-side column sort for tables marked .sortable.
//
// Activation: any <table class="sortable"> with header cells that
// carry data-sort-type ("string" | "number" | "date") and an
// optional data-sort-default ("asc" | "desc") becomes click-sortable.
// First click on a header sorts ascending (or whatever
// data-sort-default says); subsequent clicks toggle direction.
//
// No frameworks, no build step. Aria semantics follow the W3C
// ARIA Authoring Practices grid pattern: aria-sort on the active
// header so screen readers report direction.
(function () {
  function attach(table) {
    var headers = table.querySelectorAll('thead th[data-sort-type]');
    headers.forEach(function (th, idx) {
      th.classList.add('sortable-th');
      th.tabIndex = 0;
      th.addEventListener('click', function () { sort(table, idx, th); });
      th.addEventListener('keydown', function (ev) {
        if (ev.key === 'Enter' || ev.key === ' ') {
          ev.preventDefault();
          sort(table, idx, th);
        }
      });
    });
  }

  function sort(table, colIdx, th) {
    var type = th.getAttribute('data-sort-type') || 'string';
    var defaultDir = th.getAttribute('data-sort-default') || 'asc';
    // Direction state lives on the th itself so reordering a
    // different column doesn't carry stale state.
    var current = th.getAttribute('aria-sort');
    var dir;
    if (current === 'ascending') dir = 'descending';
    else if (current === 'descending') dir = 'ascending';
    else dir = defaultDir === 'desc' ? 'descending' : 'ascending';

    // Clear previous active header.
    table.querySelectorAll('thead th[aria-sort]').forEach(function (other) {
      if (other !== th) other.removeAttribute('aria-sort');
    });
    th.setAttribute('aria-sort', dir);

    var tbody = table.tBodies[0];
    if (!tbody) return;
    var rows = Array.prototype.slice.call(tbody.rows);
    rows.sort(function (a, b) {
      var av = cellValue(a.cells[colIdx], type);
      var bv = cellValue(b.cells[colIdx], type);
      var cmp = compare(av, bv, type);
      return dir === 'ascending' ? cmp : -cmp;
    });
    rows.forEach(function (r) { tbody.appendChild(r); });
  }

  function cellValue(cell, type) {
    if (!cell) return '';
    var raw = cell.getAttribute('data-sort-value');
    if (raw == null) raw = (cell.textContent || '').trim();
    if (type === 'number') {
      var n = parseFloat(raw);
      return isNaN(n) ? -Infinity : n;
    }
    if (type === 'date') {
      var t = Date.parse(raw);
      return isNaN(t) ? 0 : t;
    }
    return raw.toLowerCase();
  }

  function compare(a, b, type) {
    if (type === 'number' || type === 'date') {
      if (a < b) return -1;
      if (a > b) return 1;
      return 0;
    }
    return a.localeCompare(b);
  }

  function init() {
    document.querySelectorAll('table.sortable').forEach(attach);
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
