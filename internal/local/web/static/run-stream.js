// run-stream.js — coalesces consecutive agent_text frames in the
// run-detail SSE timeline so a streaming model turn becomes one
// growing card instead of N tiny cards.
//
// htmx swaps the new event card into #run-events with hx-swap="beforeend".
// We listen for htmx:afterSwap, look at the just-inserted node, and if
// it's an agent_text frame whose previous sibling is also an agent_text
// frame for the same role, we move the text into the previous card and
// drop the new one. Anything else (tool calls, run/node lifecycle,
// permission events) breaks the chain and lands as a fresh card.
(function () {
  function isCoalescibleFrame(el) {
    if (!el || !el.classList) return false;
    return el.classList.contains('frame-agent_text') || el.classList.contains('frame-agent_thought');
  }

  function sameFrameKind(a, b) {
    return a.getAttribute('data-frame-kind') === b.getAttribute('data-frame-kind');
  }

  function sameRole(a, b) {
    return a.getAttribute('data-frame-role') === b.getAttribute('data-frame-role');
  }

  function previousFrameSibling(el) {
    var p = el.previousElementSibling;
    while (p && p.nodeType === 1 && p.tagName !== 'ARTICLE') {
      p = p.previousElementSibling;
    }
    return p;
  }

  function coalesce(node) {
    if (!isCoalescibleFrame(node)) return;
    var prev = previousFrameSibling(node);
    if (!isCoalescibleFrame(prev) || !sameRole(prev, node) || !sameFrameKind(prev, node)) return;
    var newText = node.querySelector('[data-frame-text]');
    var prevText = prev.querySelector('[data-frame-text]');
    if (!newText || !prevText) return;
    prevText.textContent += newText.textContent;
    node.parentNode.removeChild(node);
  }

  document.body.addEventListener('htmx:afterSwap', function (ev) {
    if (!ev.detail || !ev.detail.target || ev.detail.target.id !== 'run-events') return;
    // hx-swap="beforeend" appends one or more nodes; coalesce
    // every new ARTICLE under the target's tail. We walk back
    // from the last child until we hit something we've already
    // processed (one we've already coalesced, or a stable card).
    var target = ev.detail.target;
    // Newly-inserted nodes are at the end. Process the last
    // ARTICLE specifically — htmx delivers one event per swap
    // so there's exactly one new card to look at.
    var last = target.lastElementChild;
    if (last && last.tagName === 'ARTICLE') coalesce(last);
  });
})();
