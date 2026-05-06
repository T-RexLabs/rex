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

  function isTranscriptNode(node) {
    if (!node || !node.classList) return false;
    if (node.classList.contains('frame-agent_text')) return true;
    if (node.classList.contains('event-permission')) return true;
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

    var inTranscript = isTranscriptNode(node);
    var target = inTranscript ? transcript : activity;
    var keepBottom = inTranscript && autoStick;
    target.appendChild(node);
    var merged = coalesce(node) || node;

    renderFrameText(merged);
    if (!inTranscript) {
      setActivityStatus(summarizeActivity(merged));
    } else if (merged.classList && merged.classList.contains('event-permission')) {
      setActivityStatus('awaiting permission decision');
    }

    if (keepBottom) {
      window.requestAnimationFrame(function () {
        window.scrollTo(0, document.body.scrollHeight);
      });
    }
  }

  function initComposerShortcuts() {
    var form = document.querySelector('.run-input-dock');
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
