/*!
 * rex clipboard helper.
 *
 * Wires the .acid (ACID badge) anchor's data-acid attribute to
 * navigator.clipboard.writeText(...) so click-to-copy works without
 * a heavier dependency. Falls back gracefully when clipboard API is
 * unavailable (older browsers, non-secure contexts).
 *
 * Web-ui.LOCAL.4: "ACID badges are click-to-copy".
 */
(function () {
  if (typeof document === 'undefined') return;
  document.addEventListener('click', function (ev) {
    var target = ev.target;
    if (!target || !target.classList || !target.classList.contains('acid')) return;
    ev.preventDefault();
    var value = target.getAttribute('data-acid') || target.textContent;
    if (!value) return;
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(value);
        flash(target);
        return;
      }
    } catch (_) { /* fall through */ }
    // Fallback: select + execCommand for legacy contexts.
    var range = document.createRange();
    range.selectNodeContents(target);
    var sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
    try { document.execCommand('copy'); } catch (_) {}
    sel.removeAllRanges();
    flash(target);
  });
  function flash(el) {
    var prev = el.style.background;
    el.style.transition = 'background 200ms';
    el.style.background = 'rgba(46, 111, 255, 0.25)';
    setTimeout(function () { el.style.background = prev; }, 400);
  }
})();
