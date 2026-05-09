// run-form.js switches the local run form between its shell and
// harness panels. The inactive panel is both hidden and disabled so
// users only see the controls that matter for the chosen run type.
(function () {
  function splitCSV(value) {
    if (!value) return [];
    return value.split(',').map(function (s) { return s.trim(); }).filter(Boolean);
  }

  function populateSelect(select, values) {
    while (select.firstChild) select.removeChild(select.firstChild);
    var placeholder = document.createElement('option');
    placeholder.value = '';
    placeholder.textContent = 'harness default';
    select.appendChild(placeholder);
    values.forEach(function (value) {
      var opt = document.createElement('option');
      opt.value = value;
      opt.textContent = value;
      select.appendChild(opt);
    });
  }

  function syncHarnessFields() {
    var harness = document.getElementById('run-harness');
    var modelField = document.getElementById('run-model-field');
    var model = document.getElementById('run-model');
    var modeField = document.getElementById('run-mode-field');
    var mode = document.getElementById('run-mode');
    if (!harness) return;
    var option = harness.options[harness.selectedIndex];
    var models = option ? splitCSV(option.getAttribute('data-models')) : [];
    var modes = option ? splitCSV(option.getAttribute('data-modes')) : [];

    if (modelField && model) {
      var modelValue = model.value;
      populateSelect(model, models);
      model.hidden = false;
      if (models.indexOf(modelValue) >= 0) {
        model.value = modelValue;
      }
      modelField.hidden = models.length === 0;
    }
    if (modeField && mode) {
      var modeValue = mode.value;
      populateSelect(mode, modes);
      mode.hidden = false;
      if (modes.indexOf(modeValue) >= 0) {
        mode.value = modeValue;
      }
      modeField.hidden = modes.length === 0;
    }
  }

  function syncPanels() {
    var runType = document.getElementById('run-type');
    if (!runType) return;
    var value = runType.value || 'shell';
    document.querySelectorAll('[data-run-panel]').forEach(function (panel) {
      var active = panel.getAttribute('data-run-panel') === value;
      panel.hidden = !active;
      panel.querySelectorAll('input, select, textarea').forEach(function (el) {
        el.disabled = !active;
      });
    });
    if (value === 'harness') {
      syncHarnessFields();
    }
  }

  // syncFromTaskInfo reflects the selected task's state + full
  // description into the inline info block below the dropdown.
  // The dropdown row itself only fits a truncated label;
  // populating the panel beneath gives the author confirmation
  // about exactly which task they just picked. Hidden when no
  // task is selected (the "(none)" option).
  function syncFromTaskInfo() {
    var sel = document.getElementById('run-from-task');
    var info = document.getElementById('run-from-task-info');
    if (!sel || !info) return;
    var opt = sel.options[sel.selectedIndex];
    var state = opt && opt.value ? (opt.getAttribute('data-state') || '') : '';
    var desc = opt && opt.value ? (opt.getAttribute('data-description') || '') : '';
    var stateEl = info.querySelector('[data-task-state]');
    var descEl = info.querySelector('[data-task-description]');
    if (stateEl) {
      stateEl.textContent = state;
      stateEl.className = 'task-info-state pill' + (state ? ' pill-' + state : '');
    }
    if (descEl) descEl.textContent = desc;
    info.hidden = !opt || !opt.value;
  }

  document.addEventListener('DOMContentLoaded', function () {
    var runType = document.getElementById('run-type');
    var harness = document.getElementById('run-harness');
    var fromTask = document.getElementById('run-from-task');
    if (!runType) return;
    runType.addEventListener('change', syncPanels);
    if (harness) {
      harness.addEventListener('change', syncHarnessFields);
    }
    if (fromTask) {
      fromTask.addEventListener('change', syncFromTaskInfo);
    }
    syncPanels();
    syncFromTaskInfo();
  });
})();
