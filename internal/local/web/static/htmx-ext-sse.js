/*!
 * Minimal vanilla-JS SSE shim for rex's run-detail live updates.
 *
 * Scope: implements just enough of htmx-ext-sse for the run-detail
 * page (web-ui.LIVE.1). Recognized attributes:
 *
 *   sse-connect="<url>"   element opens an EventSource to <url>
 *   sse-swap="<name>"     listen for SSE events named <name>
 *   hx-target="<sel>"     CSS selector for where the swap goes
 *                         (defaults to the element itself)
 *   hx-swap="<mode>"      one of: beforeend (default), afterbegin,
 *                         innerHTML, outerHTML
 *
 * Reconnect: the browser handles EventSource reconnect natively;
 * we close+reopen on a permanent error after a 5s backoff so the
 * tab keeps trying after the server bounces.
 *
 * Drop the upstream htmx-ext-sse@2.2.x bytes in place of this file
 * to get the full feature set (event filtering, hx-headers, etc.).
 * The template surface (sse-connect/sse-swap/hx-target/hx-swap) is
 * compatible with the upstream extension, so swapping the bytes is
 * a no-op for the page templates.
 */
(function () {
  if (typeof window === 'undefined') return;
  if (window.__rexSSEShimLoaded) return;
  window.__rexSSEShimLoaded = true;

  var BACKOFF_MS = 5000;

  function applySwap(target, mode, html) {
    if (!target) return;
    switch ((mode || 'beforeend').toLowerCase()) {
      case 'afterbegin':
        target.insertAdjacentHTML('afterbegin', html);
        break;
      case 'innerhtml':
        target.innerHTML = html;
        break;
      case 'outerhtml':
        target.outerHTML = html;
        break;
      case 'beforeend':
      default:
        target.insertAdjacentHTML('beforeend', html);
        break;
    }
  }

  function dispatchAfterSwap(target) {
    if (!target) return;
    var detail = { target: target };
    var evt;
    try {
      evt = new CustomEvent('htmx:afterSwap', { bubbles: true, detail: detail });
    } catch (_) {
      evt = document.createEvent('CustomEvent');
      evt.initCustomEvent('htmx:afterSwap', true, false, detail);
    }
    target.dispatchEvent(evt);
  }

  function resolveTarget(el) {
    var sel = el.getAttribute('hx-target');
    if (!sel) return el;
    try {
      return document.querySelector(sel) || el;
    } catch (_) {
      return el;
    }
  }

  function wire(el) {
    if (el.__rexSSEWired) return;
    el.__rexSSEWired = true;

    var url = el.getAttribute('sse-connect');
    var swapName = el.getAttribute('sse-swap');
    if (!url || !swapName) return;

    var swapMode = el.getAttribute('hx-swap') || 'beforeend';
    var es = null;
    var closed = false;

    function open() {
      if (closed) return;
      es = new EventSource(url);
      es.addEventListener(swapName, function (evt) {
        var target = resolveTarget(el);
        applySwap(target, swapMode, evt.data);
        dispatchAfterSwap(target);
      });
      es.addEventListener('error', function () {
        // Browser will retry on transient errors; on a hard
        // close (readyState === 2) we manually reopen after
        // a backoff so the tab keeps trying.
        if (es && es.readyState === 2 /* CLOSED */) {
          try { es.close(); } catch (_) {}
          es = null;
          if (!closed) setTimeout(open, BACKOFF_MS);
        }
      });
    }

    open();

    // Best-effort cleanup when the page unloads. Without this
    // long-running tabs can leak open EventSources across
    // back/forward cache navigations.
    window.addEventListener('beforeunload', function () {
      closed = true;
      if (es) try { es.close(); } catch (_) {}
    });
  }

  function scan() {
    var nodes = document.querySelectorAll('[sse-connect][sse-swap]');
    for (var i = 0; i < nodes.length; i++) {
      wire(nodes[i]);
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', scan);
  } else {
    scan();
  }
})();
