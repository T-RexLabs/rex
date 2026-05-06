// run-stream.js powers the run-detail live transcript.
//
// Behaviour in default mode:
// 1) route incoming timeline nodes into transcript vs activity lanes
// 2) coalesce consecutive agent_text/agent_thought chunks
// 3) stick to bottom only when the viewer is already near bottom
(function () {
  document.documentElement.classList.add('js');
  var autoStick = true;

  function isCoalescibleFrame(el) {
    if (!el || !el.classList) return false;
    return el.classList.contains('frame-agent_text') || el.classList.contains('frame-agent_thought');
  }

  // dropSyntheticPromptIfShadowed removes the optimistic user prompt
  // row once a real user_message_chunk lands. The synthetic row was
  // server-rendered from the form submission so the user saw their
  // message immediately; the harness echo arrives ~1s later carrying
  // the same text, so we drop the placeholder to avoid duplication.
  function dropSyntheticPromptIfShadowed(node) {
    if (!node || !node.classList) return;
    if (!node.classList.contains('frame-agent_text')) return;
    if (node.getAttribute('data-frame-role') !== 'user') return;
    if (node.getAttribute('data-synthetic') === 'prompt') return;
    var synthetic = document.querySelector('[data-synthetic="prompt"]');
    if (synthetic && synthetic.parentNode) {
      synthetic.parentNode.removeChild(synthetic);
    }
  }

  function sameFrameKind(a, b) {
    return a.getAttribute('data-frame-kind') === b.getAttribute('data-frame-kind');
  }

  function sameRole(a, b) {
    return a.getAttribute('data-frame-role') === b.getAttribute('data-frame-role');
  }

  function previousFrameSibling(el) {
    var p = el.previousElementSibling;
    while (p && p.nodeType !== 1) {
      p = p.previousElementSibling;
    }
    return p;
  }

  // findToolCard locates an existing tool card with the same
  // toolCallId so a follow-up tool_call_update can mutate it in
  // place rather than spawning a new article.
  function findToolCard(id) {
    if (!id) return null;
    var safe = (window.CSS && window.CSS.escape) ? window.CSS.escape(id) : id;
    return document.querySelector('[data-tool-call-id="' + safe + '"]');
  }

  function copyToolBody(target, source) {
    var srcBody = source.querySelector('[data-tool-body]');
    var dstBody = target.querySelector('[data-tool-body]');
    if (!srcBody || !dstBody) return;

    var srcName = srcBody.querySelector('[data-tool-name]');
    var dstName = dstBody.querySelector('[data-tool-name]');
    if (srcName && dstName) {
      var newName = (srcName.textContent || '').trim();
      // Don't overwrite a real name with "(unnamed)".
      if (newName && newName !== '(unnamed)') {
        dstName.textContent = newName;
      }
    }

    var srcSub = srcBody.querySelector('[data-tool-subtitle]');
    var dstSub = dstBody.querySelector('[data-tool-subtitle]');
    if (srcSub && dstSub) {
      var subText = (srcSub.textContent || '').trim();
      if (subText) {
        dstSub.textContent = subText;
        dstSub.removeAttribute('hidden');
      }
    }

    var srcStatus = srcBody.querySelector('[data-tool-status]');
    var dstStatus = dstBody.querySelector('[data-tool-status]');
    if (srcStatus && dstStatus) {
      var status = (srcStatus.textContent || '').trim();
      if (status) {
        dstStatus.textContent = status;
        dstStatus.className = srcStatus.className;
      }
    }

    var srcArgs = srcBody.querySelector('[data-tool-args]');
    var dstArgs = dstBody.querySelector('[data-tool-args]');
    if (srcArgs && dstArgs && srcArgs.innerHTML.trim()) {
      dstArgs.innerHTML = srcArgs.innerHTML;
    }

    var srcOut = srcBody.querySelector('[data-tool-output]');
    var dstOut = dstBody.querySelector('[data-tool-output]');
    if (srcOut && dstOut) {
      var outText = srcOut.textContent || '';
      if (outText.trim()) {
        dstOut.textContent = outText;
        dstOut.removeAttribute('hidden');
      }
    }
  }

  // mergeToolUpdate merges an incoming tool_result frame into the
  // existing card with the matching data-tool-call-id. Returns true
  // when a merge happened (caller should drop the new node).
  function mergeToolUpdate(node) {
    if (!node || !node.classList || !node.classList.contains('frame-tool_result')) return false;
    var id = node.getAttribute('data-tool-call-id');
    if (!id) return false;
    var existing = findToolCard(id);
    if (!existing || existing === node) return false;
    copyToolBody(existing, node);
    existing.classList.add('frame-tool-coalesced');
    var status = existing.querySelector('[data-tool-status]');
    if (status && (status.textContent || '').trim() === 'ok') {
      existing.classList.add('frame-tool-done');
    }
    return true;
  }

  function coalesce(node) {
    if (!isCoalescibleFrame(node)) return;
    var prev = previousFrameSibling(node);
    if (!isCoalescibleFrame(prev) || !sameRole(prev, node) || !sameFrameKind(prev, node)) return;
    var newText = node.querySelector('[data-frame-text]');
    var prevText = prev.querySelector('[data-frame-text]');
    if (!newText || !prevText) return;
    var nextRaw = newText.getAttribute('data-raw-text');
    if (nextRaw === null) nextRaw = newText.textContent || '';
    var prevRaw = prevText.getAttribute('data-raw-text');
    if (prevRaw === null) prevRaw = prevText.textContent || '';
    prevText.setAttribute('data-raw-text', prevRaw + nextRaw);
    node.parentNode.removeChild(node);
    return prev;
  }

  // The transcript is what the agent did, not just what it said.
  // Tool calls, tool results, and chain-of-thought are first-class
  // citizens in the timeline. Pure meta frames (session/update,
  // usage ticks) and lifecycle events stay in the activity panel
  // because they're operational noise, not work.
  function isTranscriptNode(node) {
    if (!node || !node.classList) return false;
    var cl = node.classList;
    if (cl.contains('frame-agent_text')) return true;
    if (cl.contains('frame-agent_thought')) return true;
    if (cl.contains('frame-tool_call')) return true;
    if (cl.contains('frame-tool_result')) return true;
    if (cl.contains('event-permission')) return true;
    if (cl.contains('permission-card')) return true;
    return false;
  }

  function nearPageBottom() {
    return window.innerHeight + window.scrollY >= document.body.offsetHeight - 120;
  }

  function escapeHTML(text) {
    return String(text || '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function renderInline(text) {
    var out = escapeHTML(text);
    out = out.replace(/`([^`]+)`/g, '<code>$1</code>');
    out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    out = out.replace(/\*([^*]+)\*/g, '<em>$1</em>');
    return out;
  }

  function renderMarkdown(raw) {
    var lines = String(raw || '').replace(/\r\n?/g, '\n').split('\n');
    var html = [];
    var inCode = false;
    var code = [];
    var inList = false;
    var para = [];

    function flushPara() {
      if (!para.length) return;
      html.push('<p>' + renderInline(para.join(' ')) + '</p>');
      para = [];
    }

    function closeList() {
      if (!inList) return;
      html.push('</ul>');
      inList = false;
    }

    function flushCode() {
      if (!inCode) return;
      html.push('<pre><code>' + escapeHTML(code.join('\n')) + '</code></pre>');
      code = [];
      inCode = false;
    }

    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      if (line.trim().indexOf('```') === 0) {
        flushPara();
        closeList();
        if (inCode) {
          flushCode();
        } else {
          inCode = true;
          code = [];
        }
        continue;
      }

      if (inCode) {
        code.push(line);
        continue;
      }

      if (!line.trim()) {
        flushPara();
        closeList();
        continue;
      }

      var heading = line.match(/^(#{1,3})\s+(.+)$/);
      if (heading) {
        flushPara();
        closeList();
        html.push('<h' + heading[1].length + '>' + renderInline(heading[2]) + '</h' + heading[1].length + '>');
        continue;
      }

      var list = line.match(/^\s*[-*]\s+(.+)$/);
      if (list) {
        flushPara();
        if (!inList) {
          html.push('<ul>');
          inList = true;
        }
        html.push('<li>' + renderInline(list[1]) + '</li>');
        continue;
      }

      closeList();
      para.push(line.trim());
    }

    flushPara();
    closeList();
    if (inCode) flushCode();
    return html.join('');
  }

  function hasMarkdownSyntax(raw) {
    if (!raw) return false;
    return /```|`[^`]+`|\*\*[^*]+\*\*|\n\s*[-*]\s+|\n#{1,3}\s+|^#{1,3}\s+/.test(raw);
  }

  function renderFrameText(node) {
    if (!node || !node.classList) return;
    if (!node.classList.contains('frame-agent_text') && !node.classList.contains('frame-agent_thought')) return;
    var body = node.querySelector('[data-frame-text]');
    if (!body) return;
    var raw = body.getAttribute('data-raw-text');
    if (raw === null) raw = body.textContent || '';
    body.setAttribute('data-raw-text', raw);

    var role = node.getAttribute('data-frame-role');
    var kind = node.getAttribute('data-frame-kind');
    if (kind === 'agent_text' && role === 'assistant' && hasMarkdownSyntax(raw)) {
      body.classList.add('frame-markdown');
      body.innerHTML = renderMarkdown(raw);
      return;
    }
    body.classList.remove('frame-markdown');
    body.textContent = raw;
  }

  function setActivityStatus(text) {
    var status = document.getElementById('run-activity-status');
    if (!status || !text) return;
    status.textContent = 'status · ' + text;
  }

  // markRunTerminal closes the activity panel once the run reaches
  // a terminal lifecycle event so a finished run doesn't leave the
  // panel sprawled open under the transcript. We only close panels
  // we ourselves auto-opened (data-run-status="running"); a user
  // who manually expanded a finished run's panel keeps it open.
  function markRunTerminal() {
    var panel = document.querySelector('.run-activity-panel');
    if (!panel) return;
    if (panel.getAttribute('data-run-status') === 'running') {
      panel.removeAttribute('open');
    }
  }

  // updateRunningToolChip syncs the "tool currently running" pill
  // shown inline in the transcript so the user has a constantly
  // visible signal during long tool calls. The chip is removed
  // when no live (pending/running) tool calls remain.
  function updateRunningToolChip() {
    var transcript = document.getElementById('run-transcript');
    if (!transcript) return;
    var live = findLiveToolCard();
    var chip = document.getElementById('run-running-tool-chip');
    if (!live) {
      if (chip && chip.parentNode) chip.parentNode.removeChild(chip);
      return;
    }
    if (!chip) {
      chip = document.createElement('aside');
      chip.id = 'run-running-tool-chip';
      chip.className = 'running-tool-chip';
      chip.innerHTML =
        '<span class="dot dot-pulse" aria-hidden="true"></span>' +
        '<span class="chip-label">running tool</span>' +
        '<code class="chip-name" data-chip-name></code>' +
        '<span class="chip-subtitle muted small" data-chip-subtitle></span>';
    }
    var nameEl = chip.querySelector('[data-chip-name]');
    var subEl = chip.querySelector('[data-chip-subtitle]');
    var nameSrc = live.querySelector('[data-tool-name]');
    var subSrc = live.querySelector('[data-tool-subtitle]');
    if (nameEl && nameSrc) nameEl.textContent = (nameSrc.textContent || '').trim() || 'tool';
    if (subEl) {
      var subText = subSrc ? (subSrc.textContent || '').trim() : '';
      if (subText) {
        subEl.textContent = '· ' + subText;
        subEl.removeAttribute('hidden');
      } else {
        subEl.textContent = '';
        subEl.setAttribute('hidden', '');
      }
    }
    if (chip.parentNode !== transcript) {
      transcript.appendChild(chip);
    } else if (chip !== transcript.lastElementChild) {
      transcript.appendChild(chip); // move to end so it stays at the cursor
    }
  }

  // findLiveToolCard returns the most recent tool card whose
  // status pill is still pending/running. Returns null when every
  // recorded tool call has completed (or errored).
  function findLiveToolCard() {
    var cards = document.querySelectorAll('[data-tool-call-id]');
    for (var i = cards.length - 1; i >= 0; i--) {
      var s = cards[i].querySelector('[data-tool-status]');
      var status = s ? (s.textContent || '').trim() : '';
      if (status === 'pending' || status === 'running') return cards[i];
    }
    return null;
  }

  function summarizeActivity(node) {
    if (!node || !node.classList) return '';
    var compact = node.querySelector('.compact-label');
    if (compact) return compact.textContent.trim();
    var kind = node.getAttribute('data-frame-kind');
    if (kind) {
      var role = node.getAttribute('data-frame-role') || 'assistant';
      if (kind === 'agent_thought') return role + ' thinking';
      if (kind === 'tool_call') return 'running tool';
      if (kind === 'tool_result') return 'tool finished';
      if (kind === 'agent_text') return role + ' replied';
    }
    var eventType = node.querySelector('.event-type');
    if (eventType) return eventType.textContent.trim();
    return '';
  }

  function updateAutoStick() {
    autoStick = nearPageBottom();
  }

  function routePendingFromSource() {
    var source = document.getElementById('run-events');
    if (!source) return;
    while (source.firstElementChild) {
      routeNode(source.firstElementChild);
    }
    source.classList.add('routed');
  }

  function routeNode(node) {
    var transcript = document.getElementById('run-transcript');
    var activity = document.getElementById('run-activity');
    if (!transcript || !activity || !node || node.nodeType !== 1) return;

    // Tool follow-ups (tool_call_update) merge into the existing
    // card by toolCallId and the new node is discarded entirely.
    if (mergeToolUpdate(node)) {
      if (node.parentNode) node.parentNode.removeChild(node);
      updateRunningToolChip();
      return;
    }

    // Drop the optimistic prompt placeholder as soon as the real
    // user_message_chunk lands; otherwise the user sees their
    // prompt rendered twice.
    dropSyntheticPromptIfShadowed(node);

    var inTranscript = isTranscriptNode(node);
    var target = inTranscript ? transcript : activity;
    var keepBottom = inTranscript && autoStick;
    target.appendChild(node);
    var merged = coalesce(node) || node;

    renderFrameText(merged);
    if (!inTranscript) {
      setActivityStatus(summarizeActivity(merged));
    } else if (merged.classList && (merged.classList.contains('event-permission') || merged.classList.contains('permission-card--pending'))) {
      setActivityStatus('permission required');
    }
    if (merged && merged.classList && merged.classList.contains('event-compact-lifecycle')) {
      var label = merged.querySelector('.compact-label code');
      var typ = label ? (label.textContent || '').trim() : '';
      if (typ === 'run.completed' || typ === 'run.cancelled' || typ === 'run.aborted') {
        markRunTerminal();
      }
    }
    updateRunningToolChip();

    if (keepBottom) {
      window.requestAnimationFrame(function () {
        window.scrollTo(0, document.body.scrollHeight);
      });
    }
  }

  function initComposerShortcuts() {
    var form = document.querySelector('.run-dock, .run-input-dock');
    if (!form) return;
    var transcript = document.getElementById('run-transcript');
    if (transcript) transcript.classList.add('has-dock');
    var input = form.querySelector('textarea[name="text"]');
    if (!input) return;
    input.addEventListener('keydown', function (ev) {
      if (ev.key !== 'Enter') return;
      if (ev.shiftKey) return;
      ev.preventDefault();
      if (!input.value.trim()) return;
      form.requestSubmit();
    });
  }

  function bootstrapRouting() {
    var transcript = document.getElementById('run-transcript');
    if (!transcript) {
      initComposerShortcuts();
      return;
    }
    updateAutoStick();
    routePendingFromSource();

    var source = document.getElementById('run-events');
    if (source && window.MutationObserver) {
      var observer = new MutationObserver(function () {
        routePendingFromSource();
      });
      observer.observe(source, { childList: true });
    }

    initComposerShortcuts();
    if (autoStick) {
      window.scrollTo(0, document.body.scrollHeight);
    }
  }

  document.body.addEventListener('htmx:afterSwap', function (ev) {
    var section = document.querySelector('[sse-connect][data-debug]');
    if (section && section.getAttribute('data-debug') === '1') return;
    if (!ev.detail || !ev.detail.target || ev.detail.target.id !== 'run-events') return;
    routePendingFromSource();
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bootstrapRouting);
  } else {
    bootstrapRouting();
  }

  window.addEventListener('scroll', updateAutoStick, { passive: true });
})();
