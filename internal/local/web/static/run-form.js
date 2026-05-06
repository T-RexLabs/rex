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

  document.addEventListener('DOMContentLoaded', function () {
    var runType = document.getElementById('run-type');
    var harness = document.getElementById('run-harness');
    if (!runType) return;
    runType.addEventListener('change', syncPanels);
    if (harness) {
      harness.addEventListener('change', syncHarnessFields);
    }
    syncPanels();
  });
})();
