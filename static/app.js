(() => {
  'use strict';

  const byId = (id) => document.getElementById(id);
  const state = { access: null, student: null, auditor: null, report: null, selectedFile: '', dirty: false, preparing: false, running: false, refreshing: false };
  const storageKey = 'live-code-audit-test-session';
  const statusLabels = { prepared: 'Ожидает начала', active: 'Идёт защита', finishing: 'Проверка итогов', finished: 'Завершён' };
  const eventLabels = { started: 'Началась защита', edited: 'Сохранён файл', tests: 'Запущены тесты', note: 'Записано объяснение', finished: 'Защита завершена' };

  function message(text, error = false) {
    const box = byId('message');
    box.textContent = text;
    box.classList.toggle('error', error);
    box.hidden = !text;
  }

  async function api(method, url, role = '', body = undefined, contentType = '') {
    const headers = {};
    if (role && state.access) headers.Authorization = `Bearer ${state.access[role + 'Token']}`;
    if (contentType) headers['Content-Type'] = contentType;
    const response = await fetch(url, { method, headers, body, cache: 'no-store' });
    const text = await response.text();
    let data;
    try { data = JSON.parse(text); } catch { data = null; }
    if (!response.ok) throw new Error(data?.error || `HTTP ${response.status}: ${text.slice(0, 200)}`);
    return data;
  }

  function setPreparing(active) {
    state.preparing = active;
    byId('create-demo').disabled = active;
    for (const button of document.querySelectorAll('#setup form button')) button.disabled = active;
  }

  async function createAudit(url, body, contentType) {
    if (state.preparing) return;
    setPreparing(true);
    const started = Date.now();
    message('Проверяем исходный проект и подбираем мутации. Это может занять несколько минут…');
    const progress = setInterval(() => {
      message(`Подбираем три мутации в Docker · прошло ${Math.floor((Date.now() - started) / 1000)} с. Не закрывайте вкладку.`);
    }, 1000);
    byId('state-badge').textContent = 'Подготовка';
    byId('state-badge').dataset.state = 'preparing';
    try {
      state.access = await api('POST', url, '', body, contentType);
      sessionStorage.setItem(storageKey, JSON.stringify(state.access));
      state.dirty = false;
      state.selectedFile = '';
      byId('audit').hidden = false;
      byId('setup').hidden = true;
      await refresh();
      message('Аудит готов. Посмотрите три мутации справа и начните десятиминутную защиту.');
    } catch (error) {
      message(`Не удалось создать аудит: ${error.message}`, true);
      byId('state-badge').textContent = 'Аудит не создан';
      byId('state-badge').dataset.state = '';
    } finally {
      clearInterval(progress);
      setPreparing(false);
    }
  }

  function editableFile(path) {
    return path.endsWith('.go') && !path.endsWith('_test.go') && !path.startsWith('vendor/');
  }

  function renderFiles() {
    const files = state.student?.files || {};
    const names = Object.keys(files).sort();
    const select = byId('file-select');
    if (!names.includes(state.selectedFile)) state.selectedFile = names.find(editableFile) || names[0] || '';
    select.replaceChildren(...names.map((name) => {
      const option = document.createElement('option');
      option.value = name;
      option.textContent = name;
      return option;
    }));
    select.value = state.selectedFile;
    if (!state.dirty) byId('code-editor').value = files[state.selectedFile] || '';
    byId('dirty-indicator').hidden = !state.dirty;
  }

  function renderTests(result) {
    if (!result) return;
    byId('test-result-label').textContent = result.timedOut ? 'Время тестов вышло' : result.passed ? 'Тесты пройдены' : 'Есть ошибки';
    const output = result.output || '';
    const lines = [];
    for (const line of output.split('\n')) {
      try {
        const entry = JSON.parse(line);
        if (entry.Action === 'output' && entry.Output) lines.push(entry.Output);
        if (entry.Action === 'build-output' && entry.Output) lines.push(entry.Output);
      } catch { /* Some Docker diagnostics are plain text. */ }
    }
    byId('test-log').textContent = lines.length ? lines.join('').trim() : output || (result.passed ? 'Тесты завершились успешно.' : 'Тесты не завершились.');
  }

  function renderMutations() {
    const list = byId('mutation-list');
    list.replaceChildren();
    for (const mutation of state.auditor?.mutations || []) {
      const card = document.createElement('div');
      card.className = 'mutation';
      const heading = document.createElement('strong');
      heading.textContent = `Мутация ${mutation.id} · ${mutation.function}()`;
      const location = document.createElement('span');
      location.className = 'location';
      location.textContent = `${mutation.file}, строка ${mutation.line}`;
      const before = document.createElement('code');
      before.textContent = mutation.before;
      const arrow = document.createTextNode(' → ');
      const after = document.createElement('code');
      after.className = 'after';
      after.textContent = mutation.after;
      const tests = document.createElement('span');
      tests.className = 'tests';
      tests.textContent = `Ломает: ${(mutation.failedTests || []).join(', ')}`;
      card.append(heading, location, before, arrow, after, tests);
      list.append(card);
    }
  }

  function renderEvents() {
    const list = byId('event-list');
    list.replaceChildren();
    const events = state.auditor?.events || [];
    if (!events.length) {
      const empty = document.createElement('p');
      empty.className = 'muted';
      empty.textContent = 'Действий пока нет.';
      list.append(empty);
      return;
    }
    for (const event of events.slice(-8).reverse()) {
      const item = document.createElement('div');
      item.className = 'event';
      const time = document.createElement('time');
      time.dateTime = event.at;
      time.textContent = new Date(event.at).toLocaleTimeString('ru-RU');
      const description = document.createElement('span');
      description.textContent = `${eventLabels[event.type] || event.type}${event.path ? ` · ${event.path}` : ''}${event.passed === true ? ' · успешно' : event.passed === false ? ' · есть ошибки' : ''}`;
      item.append(time, description);
      list.append(item);
    }
  }

  function updateTimer() {
    const box = byId('countdown');
    if (!state.student || state.student.status === 'prepared') {
      box.textContent = '10:00';
      box.classList.remove('urgent');
      return;
    }
    const deadline = new Date(state.student.deadline).getTime();
    const left = state.student.status === 'active' ? Math.max(0, Math.ceil((deadline - Date.now()) / 1000)) : 0;
    box.textContent = `${String(Math.floor(left / 60)).padStart(2, '0')}:${String(left % 60).padStart(2, '0')}`;
    box.classList.toggle('urgent', left <= 60);
  }

  function updateControls() {
    const status = state.student?.status || '';
    const active = status === 'active' && Date.now() < new Date(state.student.deadline).getTime();
    const canEdit = active && editableFile(state.selectedFile);
    byId('code-editor').disabled = !canEdit;
    byId('save-code').disabled = !canEdit || !state.dirty;
    byId('run-tests').disabled = !active || state.running;
    byId('note-text').disabled = !active;
    byId('note-line').disabled = !active;
    byId('record-note').disabled = !active;
    byId('finish-audit').disabled = !active || state.running;
    byId('start-audit').disabled = status !== 'prepared';
    byId('new-audit').disabled = state.running;
    byId('editor-state').textContent = !active ? (statusLabels[status] || 'Ожидает старта') : canEdit ? 'Можно редактировать' : 'Только чтение';
    byId('state-badge').textContent = statusLabels[status] || status;
    byId('state-badge').dataset.state = status;
    updateTimer();
  }

  function reportField(label, value) {
    const box = document.createElement('div');
    box.className = 'report-field';
    const title = document.createElement('b');
    title.textContent = label;
    const detail = document.createElement('span');
    detail.textContent = value;
    box.append(title, detail);
    return box;
  }

  function renderReport(report) {
    const panel = byId('report-panel');
    panel.hidden = false;
    const content = byId('report-content');
    content.replaceChildren();
    const grid = document.createElement('div');
    grid.className = 'report-grid';
    grid.append(
      reportField('Проверка в конце', report.testError || (report.tests?.passed ? 'Тесты пройдены' : 'Остались ошибки')),
      reportField('Завершение', report.reason === 'deadline' ? 'Время вышло' : 'Завершено вручную'),
    );
    content.append(grid);
    const notesTitle = document.createElement('h3');
    notesTitle.textContent = 'Объяснения студента';
    content.append(notesTitle);
    if (!report.notes?.length) {
      const empty = document.createElement('p');
      empty.className = 'muted';
      empty.textContent = 'Объяснений пока нет.';
      content.append(empty);
    }
    for (const note of report.notes || []) {
      const paragraph = document.createElement('p');
      paragraph.textContent = `${note.path ? `${note.path}:${note.line} · ` : ''}${note.text}`;
      content.append(paragraph);
    }
    const changesTitle = document.createElement('h3');
    changesTitle.textContent = 'Исходник и результат студента';
    content.append(changesTitle);
    for (const change of report.changes || []) {
      const details = document.createElement('details');
      const summary = document.createElement('summary');
      summary.textContent = change.path;
      const before = document.createElement('pre');
      before.textContent = `До мутаций:\n${change.before}`;
      const after = document.createElement('pre');
      after.textContent = `После защиты:\n${change.after}`;
      details.append(summary, before, after);
      content.append(details);
    }
  }

  async function refresh() {
    if (!state.access || state.refreshing) return;
    state.refreshing = true;
    const base = `/v1/audits/${encodeURIComponent(state.access.id)}`;
    try {
      const [student, auditor] = await Promise.all([
        api('GET', base, 'student'),
        api('GET', base, 'auditor'),
      ]);
      if (state.student && student.status === state.student.status && student.revision < state.student.revision) return;
      state.student = student;
      state.auditor = auditor;
      if (student.status !== 'active' && state.dirty) {
        state.dirty = false;
        message('Время защиты истекло. Несохранённая правка не попала в отчёт.', true);
      }
      byId('session-id').textContent = `ID: ${state.access.id.slice(0, 14)}…`;
      renderFiles();
      renderMutations();
      renderEvents();
      if (student.lastRun && !state.running) renderTests(student.lastRun);
      updateControls();
      if (student.status === 'finished' && !state.report) {
        state.report = await api('GET', `${base}/report`, 'auditor');
        renderReport(state.report);
      }
    } finally {
      state.refreshing = false;
    }
  }

  async function saveCode() {
    if (!state.dirty || !state.student || state.student.status !== 'active') return;
    const source = byId('code-editor').value;
    const path = state.selectedFile;
    const result = await api('PUT', `/v1/audits/${state.access.id}/files`, 'student', JSON.stringify({ path, source }), 'application/json');
    state.student.files[path] = source;
    state.student.revision = result.revision;
    state.student.lastRun = null;
    state.dirty = false;
    byId('dirty-indicator').hidden = true;
    byId('test-result-label').textContent = 'Код изменился · запустите тесты';
    updateControls();
  }

  async function act(task) {
    try { await task(); } catch (error) { message(error.message, true); }
  }

  byId('create-demo').addEventListener('click', () => createAudit('/v1/audits/demo'));
  byId('zip-form').addEventListener('submit', (event) => {
    event.preventDefault();
    const file = byId('zip-file').files[0];
    if (!file) return message('Выберите ZIP-архив Go-проекта.', true);
    createAudit('/v1/audits', file, 'application/zip');
  });
  byId('github-form').addEventListener('submit', (event) => {
    event.preventDefault();
    const url = byId('github-url').value.trim();
    if (!url) return message('Укажите ссылку на публичный GitHub-репозиторий.', true);
    createAudit('/v1/audits/github', JSON.stringify({ url }), 'application/json');
  });
  byId('file-select').addEventListener('change', (event) => {
    const next = event.target.value;
    if (state.dirty && !window.confirm('Текущие правки не сохранены. Переключить файл?')) {
      event.target.value = state.selectedFile;
      return;
    }
    state.selectedFile = next;
    state.dirty = false;
    byId('code-editor').value = state.student.files[next] || '';
    byId('dirty-indicator').hidden = true;
    updateControls();
  });
  byId('code-editor').addEventListener('input', () => {
    state.dirty = byId('code-editor').value !== (state.student?.files[state.selectedFile] || '');
    byId('dirty-indicator').hidden = !state.dirty;
    updateControls();
  });
  byId('save-code').addEventListener('click', () => act(async () => {
    await saveCode();
    await refresh();
    message('Правка сохранена.');
  }));
  byId('run-tests').addEventListener('click', () => act(async () => {
    if (state.running) return;
    await saveCode();
    state.running = true;
    updateControls();
    message('Тесты запускаются в Docker. Результат появится после завершения.');
    try {
      const result = await api('POST', `/v1/audits/${state.access.id}/tests`, 'student');
      renderTests(result);
      message(result.passed ? 'Тесты прошли. Объясните найденные причины перед завершением.' : 'Тесты обнаружили ошибки. Изучите вывод и исправьте код.');
    } finally {
      state.running = false;
      await refresh();
      updateControls();
    }
  }));
  byId('note-form').addEventListener('submit', (event) => {
    event.preventDefault();
    act(async () => {
      const text = byId('note-text').value.trim();
      if (!text) throw new Error('Напишите, что вы нашли и как исправили.');
      const line = Number(byId('note-line').value);
      const note = line > 0 ? { text, path: state.selectedFile, line } : { text };
      await api('POST', `/v1/audits/${state.access.id}/notes`, 'student', JSON.stringify(note), 'application/json');
      byId('note-text').value = '';
      byId('note-line').value = '';
      await refresh();
      message('Объяснение записано.');
    });
  });
  byId('start-audit').addEventListener('click', () => act(async () => {
    await api('POST', `/v1/audits/${state.access.id}/start`, 'auditor');
    await refresh();
    message('Защита началась. У студента десять минут.');
    byId('code-editor').focus();
  }));
  byId('finish-audit').addEventListener('click', () => act(async () => {
    if (!window.confirm('Завершить защиту и проверить итоговый код?')) return;
    await saveCode();
    message('Выполняется заключительная проверка проекта в Docker…');
    await api('POST', `/v1/audits/${state.access.id}/finish`, 'student');
    await refresh();
    message('Защита завершена. Результат доступен аудитору.');
    byId('report-panel').scrollIntoView({ behavior: 'smooth', block: 'start' });
  }));
  byId('refresh-audit').addEventListener('click', () => act(async () => {
    await refresh();
    message('Данные обновлены.');
  }));
  byId('download-report').addEventListener('click', () => act(async () => {
    if (!state.report) state.report = await api('GET', `/v1/audits/${state.access.id}/report`, 'auditor');
    const blob = new Blob([JSON.stringify(state.report, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = 'live-code-audit-report.json';
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }));
  byId('new-audit').addEventListener('click', () => {
    if (state.student?.status === 'active' && !window.confirm('Оставить текущую защиту? Она продолжится на сервере.')) return;
    sessionStorage.removeItem(storageKey);
    state.access = null;
    state.student = null;
    state.auditor = null;
    state.report = null;
    state.selectedFile = '';
    state.dirty = false;
    byId('audit').hidden = true;
    byId('setup').hidden = false;
    byId('report-panel').hidden = true;
    byId('test-result-label').textContent = 'Ещё не запускались';
    byId('test-log').textContent = 'После старта защиты здесь появится вывод go test.';
    byId('state-badge').textContent = 'Аудит не создан';
    byId('state-badge').dataset.state = '';
    message('Можно создать следующий аудит.');
  });

  setInterval(() => {
    if (!state.access || !state.student) return;
    updateTimer();
    if (state.student.status === 'active' && Date.now() >= new Date(state.student.deadline).getTime()) {
      updateControls();
    }
  }, 1000);
  setInterval(() => {
    if (state.access && ['active', 'finishing'].includes(state.student?.status)) {
      refresh().catch((error) => message(error.message, true));
    }
  }, 5000);

  try {
    const remembered = sessionStorage.getItem(storageKey);
    if (remembered) {
      state.access = JSON.parse(remembered);
      if (state.access?.id && state.access?.auditorToken && state.access?.studentToken) {
        byId('setup').hidden = true;
        byId('audit').hidden = false;
        refresh().catch(() => {
          sessionStorage.removeItem(storageKey);
          state.access = null;
          byId('setup').hidden = false;
          byId('audit').hidden = true;
          message('Сессия недоступна после перезапуска сервера. Создайте новый аудит.', true);
        });
      }
    }
  } catch {
    sessionStorage.removeItem(storageKey);
  }
  document.documentElement.dataset.uiReady = 'true';
})();
