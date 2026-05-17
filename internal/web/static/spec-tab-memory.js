// spec-tab-memory.js
//
// Sticky-tab shim for the spec detail page. The tabs (rendered /
// ask / yaml source / tasks / runs) are server-rendered against
// ?tab=<name>; clicking off the page and back drops you on the
// default ("rendered") because the bare URL has no tab param.
// That makes the "click into a run from the runs tab, hit back,
// scroll back to runs" flow needlessly noisy.
//
// We key per-pathname so each spec remembers its own last tab
// independently, and only redirect when the stored tab is
// non-default — landing fresh on "rendered" stays a no-op so the
// shim doesn't introduce a redirect for the common case.
//
// JS-disabled view degrades gracefully (web-ui.ACCESS.3) — no
// stickiness, but every other link works.
(function () {
  var tabsNav = document.querySelector('nav.tabs[aria-label="spec view"]');
  if (!tabsNav) return;

  var path = window.location.pathname;
  var params = new URLSearchParams(window.location.search);
  var currentTab = params.get('tab');
  var storageKey = 'rex.spec-tab:' + path;
  var defaultTab = 'rendered';

  if (!currentTab) {
    var stored = null;
    try { stored = sessionStorage.getItem(storageKey); } catch (e) { /* private mode */ }
    if (stored && stored !== defaultTab) {
      params.set('tab', stored);
      var qs = params.toString();
      var target = path + (qs ? '?' + qs : '') + window.location.hash;
      window.location.replace(target);
      return;
    }
  } else {
    try { sessionStorage.setItem(storageKey, currentTab); } catch (e) { /* private mode */ }
  }

  // Update storage on click so the destination page (which won't
  // have run this script yet) still sees the latest selection
  // when the user lands back here.
  tabsNav.addEventListener('click', function (e) {
    var a = e.target.closest && e.target.closest('a.tab');
    if (!a) return;
    var href = a.getAttribute('href') || '';
    var m = href.match(/[?&]tab=([^&#]+)/);
    if (!m) return;
    try { sessionStorage.setItem(storageKey, decodeURIComponent(m[1])); } catch (err) { /* ignore */ }
  });
})();
