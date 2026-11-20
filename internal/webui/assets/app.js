(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const platforms = { netease: '网易云音乐', qq: 'QQ 音乐', kugou: '酷狗音乐' };
  const titles = { overview: '任务概览', search: '歌曲检索', history: '运行记录', settings: '系统设置' };
  const qualities = {
    netease: [['lossless', '无损 · lossless'], ['hires', 'Hi-Res · hires'], ['exhigh', '高品质 · exhigh'], ['standard', '标准 · standard'], ['jymaster', '母带 · jymaster'], ['jyeffect', '沉浸环绕 · jyeffect'], ['sky', '沉浸声 · sky']],
    qq: [['flac', '无损 · flac'], ['hires', 'Hi-Res · hires'], ['320k', '320 kbps · 320k'], ['128k', '128 kbps · 128k'], ['master', '母带 · master']],
    kugou: [['flac', '无损 · flac'], ['hires', 'Hi-Res · hires'], ['320k', '320 kbps · 320k'], ['128k', '128 kbps · 128k'], ['master', '母带 · master']]
  };
  const statusInfo = {
    success: ['同步成功', 'success'], partial: ['部分失败', 'warning'], failed: ['同步失败', 'error'],
    running: ['正在同步', 'running'], canceled: ['已取消', 'neutral'], interrupted: ['运行中断', 'warning'],
    queued: ['等待执行', 'running'], disabled: ['已停用', 'neutral']
  };
  const state = {
    overview: null, route: '', editTask: null, deleteTask: null,
    taskSignature: '', historySignature: '', historyOptionsSignature: '', settingsDirty: false,
    busy: new Set(), overviewVersion: 0, overviewController: null, overviewPromise: null,
    previewController: null, pollTimer: null, toastTimer: null, dialogTriggers: new Map(),
    search: { keyword: '', platform: 'netease', offset: 0, limit: 20, tracks: [], loaded: false, loading: false, controller: null }
  };

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = String(text);
    return node;
  }

  function icon(name) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'icon');
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', `#icon-${name}`);
    svg.append(use);
    return svg;
  }

  function text(id, value) { $(id).textContent = value === undefined || value === null ? '—' : String(value); }
  function show(id, visible) { $(id).hidden = !visible; }
  function number(value) { return typeof value === 'number' && Number.isFinite(value) ? new Intl.NumberFormat('zh-CN').format(value) : '—'; }
  function bytes(value) {
    if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return '—';
    if (value === 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const power = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
    return `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: power > 0 ? 1 : 0 }).format(value / (1024 ** power))} ${units[power]}`;
  }

  function date(value, compact = false) {
    if (!value || String(value).startsWith('0001-')) return '—';
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) return '—';
    const options = { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false };
    if (!compact) options.year = 'numeric';
    const timezone = state.overview && state.overview.timezone;
    if (timezone) options.timeZone = timezone;
    try { return new Intl.DateTimeFormat('zh-CN', options).format(parsed); }
    catch { delete options.timeZone; return new Intl.DateTimeFormat('zh-CN', options).format(parsed); }
  }

  function duration(value) {
    if (!Number.isFinite(value) || value <= 0) return '—';
    const seconds = Math.floor(value);
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
  }

  function artistNames(track) {
    return Array.isArray(track.artists) && track.artists.length ? track.artists.map(String).join(' / ') : '—';
  }

  function statusBadge(status) {
    const info = statusInfo[status] || ['尚未运行', 'neutral'];
    return element('span', `status-badge status-${info[1]}`, info[0]);
  }

  function platformBadge(platform) {
    const known = Object.hasOwn(platforms, platform);
    return element('span', `platform-badge${known ? ` platform-${platform}` : ''}`, known ? platforms[platform] : '未知平台');
  }

  function toast(message, isError = false) {
    clearTimeout(state.toastTimer);
    const box = element('div', `toast${isError ? ' toast-error' : ''}`);
    box.append(icon(isError ? 'info' : 'check'), element('span', '', message));
    $('toast-region').replaceChildren(box);
    state.toastTimer = setTimeout(() => $('toast-region').replaceChildren(), isError ? 7000 : 4500);
  }

  function globalError(message) {
    text('global-error-message', message);
    show('global-error', Boolean(message));
  }

  async function request(path, options = {}) {
    const method = options.method || 'GET';
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), options.timeout || 90000);
    const relayAbort = () => controller.abort();
    if (options.signal) {
      if (options.signal.aborted) controller.abort();
      else options.signal.addEventListener('abort', relayAbort, { once: true });
    }
    const headers = { Accept: 'application/json', 'X-Meloport-Request': '1' };
    const settings = { method, headers, credentials: 'same-origin', mode: 'same-origin', redirect: 'error', cache: 'no-store', signal: controller.signal };
    if (method !== 'GET' && method !== 'HEAD') {
      headers['Content-Type'] = 'application/json';
      headers['X-Meloport-Request'] = '1';
      settings.body = JSON.stringify(options.body === undefined ? {} : options.body);
    }
    try {
      const response = await fetch(path, settings);
      if (response.status === 204) return null;
      let body;
      try { body = await response.json(); }
      catch { throw new Error(`服务返回了无法识别的响应（HTTP ${response.status}）。请检查服务日志。`); }
      if (!response.ok) throw new Error(typeof body.error === 'string' ? body.error.slice(0, 1500) : `请求失败（HTTP ${response.status}）。`);
      return body;
    } catch (error) {
      if (controller.signal.aborted) {
        if (options.signal && options.signal.aborted) throw new DOMException('请求已取消', 'AbortError');
        throw new Error('请求超时，请稍后重试或检查服务日志。');
      }
      if (error instanceof TypeError) throw new Error('无法连接本地服务，请确认 Meloport 正在运行。');
      throw error;
    } finally {
      clearTimeout(timeout);
      if (options.signal) options.signal.removeEventListener('abort', relayAbort);
    }
  }

  async function busyButton(button, label, operation) {
    if (button.disabled) return;
    const children = [...button.childNodes];
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    button.classList.add('is-loading');
    button.replaceChildren(document.createTextNode(label));
    try { return await operation(); }
    finally {
      button.replaceChildren(...children);
      button.disabled = false;
      button.removeAttribute('aria-busy');
      button.classList.remove('is-loading');
    }
  }

  function schedulePoll() {
    clearTimeout(state.pollTimer);
    if (!document.hidden) state.pollTimer = setTimeout(() => refreshOverview({ quiet: true }), 5000);
  }

  function refreshOverview({ quiet = false, force = false } = {}) {
    if (state.overviewPromise && !force) return state.overviewPromise;
    if (force && state.overviewController) state.overviewController.abort();
    const version = ++state.overviewVersion;
    const controller = new AbortController();
    state.overviewController = controller;
    const operation = (async () => {
      try {
        const data = await request('/api/overview', { signal: controller.signal, timeout: 15000 });
        if (version !== state.overviewVersion) return;
        if (!data || !Array.isArray(data.tasks) || !Array.isArray(data.runs) || !data.stats) throw new Error('服务返回的控制台数据结构不完整，请检查版本与服务日志。');
        state.overview = data;
        $('connection-state').className = 'connection-state is-connected';
        text('connection-label', '服务已连接');
        text('updated-at', `更新于 ${new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }).format(new Date())}`);
        globalError('');
        renderOverview();
      } catch (error) {
        if (error.name === 'AbortError') return;
        $('connection-state').className = 'connection-state is-offline';
        text('connection-label', '连接中断');
        globalError(`${error.message}${state.overview ? ' 当前保留上次成功读取的数据。' : ''}`);
        show('tasks-loading', false);
        if (!quiet && state.overview) toast(error.message, true);
      } finally {
        if (version === state.overviewVersion) {
          state.overviewController = null;
          state.overviewPromise = null;
          schedulePoll();
        }
      }
    })();
    state.overviewPromise = operation;
    return operation;
  }

  function renderOverview() {
    const data = state.overview;
    if (!data) return;
    text('stat-tasks', number(data.stats.tasks));
    text('stat-enabled', number(data.stats.enabled));
    text('stat-downloaded', number(data.stats.downloaded));
    text('stat-bytes', bytes(data.stats.bytes));
    text('nav-task-count', number(data.stats.tasks));
    text('task-count', data.tasks.length);
    text('task-timezone', `默认 ${data.schedule} · ${data.timezone}`);
    $('add-task-button').disabled = false;
    const queue = data.queue || {};
    const running = Array.isArray(queue.running) ? queue.running.length : 0;
    const pending = Array.isArray(queue.pending) ? queue.pending.length : 0;
    text('queue-summary', running || pending ? `正在同步 ${running} 项 · 排队 ${pending} 项` : '任务串行执行，当前队列空闲');
    show('key-notice', !data.api_key_configured);
    text('key-notice-text', `平台检索和歌单预览仍可使用。下载解析前，请在服务运行环境中配置 ${data.api_key_env || 'CHKSZ_API_KEY'}。`);
    show('queue-notice', Boolean(queue.paused));
    if (queue.paused) {
      text('queue-notice-text', queue.restart_required ? '解析访问已暂停。请修复密钥或权限后重启服务；平台检索和歌单预览仍可使用。' : `解析队列已暂停${date(queue.resume_at) !== '—' ? `，最早于 ${date(queue.resume_at)} 后恢复接收定时任务` : '，请检查服务日志'}。暂停期间不会堆积补跑。`);
    }
    const canRun = data.tasks.some((task) => task.enabled && !task.running && !task.queued) && !queue.paused;
    $('run-all-button').disabled = !canRun || state.busy.has('run-all');
    $('run-all-button').title = queue.paused ? '解析队列当前已暂停' : !data.api_key_configured ? '未配置解析密钥时，缺失歌曲无法下载' : '';
    renderTasks();
    renderHistory();
    renderSettings();
  }

  function taskInput(task) {
    return { id: String(task.id), platform: task.platform || 'netease', playlist: task.playlist || '', schedule: task.schedule || '', quality: task.quality || '', directory: task.directory || '', enabled: Boolean(task.enabled) };
  }

  function cell(label) {
    const node = element('td');
    node.dataset.label = label;
    return node;
  }

  function actionButton(label, iconName, className, handler) {
    const button = element('button', className || 'button button-small button-secondary');
    button.type = 'button';
    if (iconName) button.append(icon(iconName));
    if (label) button.append(document.createTextNode(label));
    button.addEventListener('click', handler);
    return button;
  }

  function rememberFocus(container, operation) {
    const active = document.activeElement;
    const key = container.contains(active) ? active.dataset.focusKey : null;
    operation();
    if (key && !active.isConnected) {
      const replacement = [...container.querySelectorAll('[data-focus-key]')].find((node) => node.dataset.focusKey === key);
      if (replacement && !replacement.disabled) replacement.focus({ preventScroll: true });
    }
  }

  function renderTasks(force = false) {
    if (!state.overview) return;
    const filter = $('task-filter').value.trim().toLocaleLowerCase();
    const tasks = state.overview.tasks;
    const visible = tasks.filter((task) => `${task.id} ${task.playlist} ${platforms[task.platform] || ''}`.toLocaleLowerCase().includes(filter));
    const signature = JSON.stringify([visible, filter, [...state.busy], state.overview.api_key_configured, state.overview.queue && state.overview.queue.paused]);
    show('tasks-loading', false);
    show('tasks-empty', tasks.length === 0);
    show('tasks-no-match', tasks.length > 0 && visible.length === 0);
    show('tasks-table', visible.length > 0);
    if (!force && signature === state.taskSignature) return;
    state.taskSignature = signature;
    rememberFocus($('tasks-body'), () => {
      const rows = visible.map((task) => {
        const row = element('tr');
        const identity = cell('任务 / 歌单');
        identity.append(element('div', 'cell-title', task.id));
        const playlist = element('span', 'cell-subtitle playlist-value', task.playlist);
        playlist.title = task.playlist;
        identity.append(playlist);
        const platform = cell('音乐平台');
        platform.append(platformBadge(task.platform));
        const toggleLabel = element('label', 'task-switch');
        const toggle = element('input');
        toggle.type = 'checkbox';
        toggle.checked = Boolean(task.enabled);
        toggle.disabled = state.busy.has(`task:${task.id}:toggle`);
        toggle.setAttribute('aria-label', `${task.enabled ? '停用' : '启用'}任务 ${task.id}`);
        toggle.dataset.focusKey = `${task.id}:toggle`;
        toggle.addEventListener('change', () => toggleTask(task));
        toggleLabel.append(toggle, element('span', '', state.busy.has(`task:${task.id}:toggle`) ? '正在保存' : task.enabled ? '已启用' : '已停用'));
        platform.append(toggleLabel);
        const schedule = cell('同步计划');
        schedule.append(element('span', 'mono', task.effective_schedule || task.schedule || state.overview.schedule), element('span', 'cell-subtitle', task.schedule ? '独立时间' : '继承全局'));
        const latest = cell('最近运行');
        latest.append(statusBadge(task.running ? 'running' : task.queued ? 'queued' : task.last_run ? task.last_run.status : ''));
        if (task.last_run) latest.append(element('span', 'cell-subtitle', date(task.last_run.started_at, true)));
        const next = cell('下次执行');
        next.append(element('span', '', task.enabled ? date(task.next_run, true) : '—'));
        const quality = task.effective_quality || task.quality || state.overview.quality;
        next.append(element('span', 'cell-subtitle', quality ? `音质 ${quality}` : '继承全局音质'));
        const management = cell('管理任务');
        const actions = element('div', 'task-actions');
        const run = actionButton(task.running ? '同步中' : task.queued ? '已排队' : '同步', 'play', '', (event) => runTask(task, event.currentTarget));
        run.dataset.focusKey = `${task.id}:run`;
        run.disabled = !task.enabled || task.running || task.queued || Boolean(state.overview.queue && state.overview.queue.paused) || state.busy.has(`task:${task.id}:run`);
        run.title = !task.enabled ? '请先启用任务' : !state.overview.api_key_configured ? '未配置解析密钥时，缺失歌曲无法下载' : '';
        const preview = actionButton('预览', 'eye', '', (event) => previewTask(task, event.currentTarget));
        preview.dataset.focusKey = `${task.id}:preview`;
        const edit = actionButton('', 'edit', 'icon-button', (event) => openTask(task, event.currentTarget));
        edit.setAttribute('aria-label', `编辑任务 ${task.id}`);
        edit.title = '编辑任务';
        edit.dataset.focusKey = `${task.id}:edit`;
        const remove = actionButton('', 'trash', 'icon-button danger', (event) => openDelete(task, event.currentTarget));
        remove.setAttribute('aria-label', `删除任务 ${task.id}`);
        remove.title = task.running || task.queued ? '任务运行或排队中，暂不可删除' : '删除任务配置';
        remove.disabled = task.running || task.queued || state.busy.has(`task:${task.id}:delete`);
        remove.dataset.focusKey = `${task.id}:delete`;
        actions.append(run, preview, edit, remove);
        management.append(actions);
        row.append(identity, platform, schedule, latest, next, management);
        return row;
      });
      $('tasks-body').replaceChildren(...rows);
    });
  }

  async function toggleTask(task) {
    const key = `task:${task.id}:toggle`;
    if (state.busy.has(key)) return;
    state.busy.add(key);
    renderTasks(true);
    try {
      await request(`/api/tasks/${encodeURIComponent(task.id)}`, { method: 'PUT', body: { ...taskInput(task), enabled: !task.enabled } });
      await refreshOverview({ force: true });
      toast(`任务 ${task.id} 已${task.enabled ? '停用' : '启用'}`);
    } catch (error) { toast(error.message, true); }
    finally { state.busy.delete(key); renderTasks(true); }
  }

  async function runTask(task, button) {
    const key = `task:${task.id}:run`;
    if (state.busy.has(key)) return;
    state.busy.add(key);
    try {
      await busyButton(button, '正在排队', async () => {
        await request(`/api/tasks/${encodeURIComponent(task.id)}/run`, { method: 'POST' });
        await refreshOverview({ force: true });
        toast(`任务 ${task.id} 已提交到同步队列`);
      });
    } catch (error) { toast(error.message, true); }
    finally { state.busy.delete(key); renderTasks(true); }
  }

  function openDialog(id, trigger) {
    const dialog = $(id);
    state.dialogTriggers.set(id, trigger || document.activeElement);
    if (!dialog.open) dialog.showModal();
  }

  function closeDialog(id) {
    const dialog = $(id);
    if (dialog.getAttribute('aria-busy') === 'true') return;
    dialog.close();
  }

  function renderTaskQuality(platform, selected = '') {
    const options = [element('option', '', '继承全局音质')];
    options[0].value = '';
    for (const [value, label] of qualities[platform] || qualities.netease) {
      const option = element('option', '', label);
      option.value = value;
      options.push(option);
    }
    $('task-quality').replaceChildren(...options);
    $('task-quality').value = options.some((option) => option.value === selected) ? selected : '';
    text('task-playlist-hint', platform === 'qq' ? '支持 QQ 音乐完整歌单链接或歌单 ID。' : platform === 'kugou' ? '支持酷狗歌单分享码、专题 ID（id_开头）或完整分享链接。' : '支持网易云完整歌单链接或歌单 ID。');
  }

  function clearErrors(prefix) {
    const root = prefix === 'task' ? $('task-form') : $('settings-form');
    root.querySelectorAll('[aria-invalid]').forEach((node) => node.removeAttribute('aria-invalid'));
    root.querySelectorAll('.field-error').forEach((node) => { node.hidden = true; node.textContent = ''; });
    const summary = prefix === 'task' ? $('task-form-error') : $('settings-error');
    summary.hidden = true;
    summary.replaceChildren();
  }

  function formErrors(prefix, errors) {
    const summary = prefix === 'task' ? $('task-form-error') : $('settings-error');
    summary.replaceChildren(element('p', '', '请检查以下内容后重试：'));
    for (const error of errors) {
      if (error.field && $(error.field)) {
        const field = $(error.field);
        field.setAttribute('aria-invalid', 'true');
        const inline = $(`${error.field}-error`);
        if (inline) { inline.textContent = error.message; inline.hidden = false; }
        const link = element('a', '', error.message);
        link.href = `#${error.field}`;
        link.addEventListener('click', (event) => {
          event.preventDefault();
          const disclosure = field.closest('details');
          if (disclosure) disclosure.open = true;
          field.focus();
        });
        summary.append(link);
      } else summary.append(element('p', '', error.message));
    }
    summary.hidden = false;
    summary.focus();
  }

  function validCron(value) { return value.trim().split(/\s+/).length === 5 && !value.includes('=') && !value.startsWith('@'); }

  function openTask(task, trigger) {
    state.editTask = task;
    clearErrors('task');
    $('task-form').reset();
    $('task-id').value = task ? task.id : '';
    $('task-id').readOnly = Boolean(task);
    $('task-platform').value = task ? task.platform : 'netease';
    $('task-playlist').value = task ? task.playlist : '';
    $('task-schedule').value = task ? task.schedule || '' : '';
    $('task-directory').value = task ? task.directory || '' : '';
    $('task-enabled').checked = task ? Boolean(task.enabled) : true;
    $('task-form').querySelector('.advanced-fields').open = Boolean(task && (task.schedule || task.quality || task.directory));
    renderTaskQuality($('task-platform').value, task ? task.quality || '' : '');
    text('task-dialog-title', task ? '编辑订阅任务' : '新建订阅任务');
    text('task-submit', task ? '保存修改' : '创建任务');
    text('task-schedule-hint', `五字段 cron；留空继承 ${state.overview ? state.overview.schedule : '全局时间'}。`);
    $('task-schedule').placeholder = state.overview ? `继承 ${state.overview.schedule}` : '留空继承全局时间';
    openDialog('task-dialog', trigger);
    (task ? $('task-playlist') : $('task-id')).focus();
  }

  async function saveTask(event) {
    event.preventDefault();
    if ($('task-submit').disabled) return;
    clearErrors('task');
    const input = {
      id: $('task-id').value.trim(), platform: $('task-platform').value, playlist: $('task-playlist').value.trim(),
      schedule: $('task-schedule').value.trim(), quality: $('task-quality').value, directory: $('task-directory').value.trim(), enabled: $('task-enabled').checked
    };
    const errors = [];
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(input.id) || /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i.test(input.id)) errors.push({ field: 'task-id', message: '任务标识需为 1–64 位字母、数字、下划线或短横线，并避免系统保留名称。' });
    if (!state.editTask && state.overview && state.overview.tasks.some((task) => String(task.id).toLowerCase() === input.id.toLowerCase())) errors.push({ field: 'task-id', message: '这个任务标识已经存在，请使用其他标识。' });
    if (!input.playlist) errors.push({ field: 'task-playlist', message: '请填写歌单链接、ID 或分享码。' });
    if (input.schedule && !validCron(input.schedule)) errors.push({ field: 'task-schedule', message: '请填写五字段 cron；也可留空继承全局时间。' });
    if (errors.length) { formErrors('task', errors); return; }
    const editing = state.editTask;
    try {
      await busyButton($('task-submit'), '正在保存', async () => {
        $('task-fields').disabled = true;
        $('task-dialog').setAttribute('aria-busy', 'true');
        const path = editing ? `/api/tasks/${encodeURIComponent(editing.id)}` : '/api/tasks';
        await request(path, { method: editing ? 'PUT' : 'POST', body: input });
        $('task-dialog').removeAttribute('aria-busy');
        $('task-dialog').close();
        await refreshOverview({ force: true });
        toast(editing ? `任务 ${input.id} 已更新` : `任务 ${input.id} 已创建`);
      });
    } catch (error) { formErrors('task', [{ message: error.message }]); }
    finally { $('task-fields').disabled = false; $('task-dialog').removeAttribute('aria-busy'); }
  }

  async function previewTask(task, trigger) {
    if (state.previewController) state.previewController.abort();
    const controller = new AbortController();
    state.previewController = controller;
    text('preview-title', `歌单预览 · ${task.id}`);
    show('preview-loading', true);
    show('preview-content', false);
    show('preview-error', false);
    $('preview-body').replaceChildren();
    openDialog('preview-dialog', trigger);
    try {
      const playlist = await request(`/api/tasks/${encodeURIComponent(task.id)}/playlist`, { signal: controller.signal, timeout: 120000 });
      if (controller.signal.aborted) return;
      if (!playlist || !Array.isArray(playlist.tracks)) throw new Error('歌单返回了无法识别的曲目列表。');
      const tracks = playlist.tracks;
      const reported = typeof playlist.reported_count === 'number' ? playlist.reported_count : tracks.length;
      text('preview-title', playlist.name || `歌单预览 · ${task.id}`);
      text('preview-summary', `${platforms[task.platform] || task.platform} · 共 ${number(reported)} 首歌曲`);
      const source = playlist.fetched_via === 'chksz_fallback' ? ['ChKSz 回退', 'warning'] : playlist.fetched_via === 'native' ? ['平台直连', 'success'] : ['来源未返回', 'neutral'];
      text('preview-source', source[0]);
      $('preview-source').className = `status-badge status-${source[1]}`;
      const rows = tracks.slice(0, 20).map((track) => {
        const row = element('tr');
        const song = cell('歌曲');
        song.append(element('div', 'cell-title', track.name || '未命名歌曲'), element('code', 'cell-subtitle mono', track.id));
        const artist = cell('歌手'); artist.textContent = artistNames(track);
        const album = cell('专辑'); album.textContent = track.album || '—';
        row.append(song, artist, album);
        return row;
      });
      $('preview-body').replaceChildren(...rows);
      show('preview-table', tracks.length > 0);
      show('preview-empty', tracks.length === 0);
      text('preview-limit-note', tracks.length > 20 ? `预览仅显示前 20 首；本次已取得 ${number(tracks.length)} 首曲目信息。` : `本次已取得 ${number(tracks.length)} 首曲目信息。预览不会下载音频。`);
      show('preview-content', true);
    } catch (error) {
      if (error.name !== 'AbortError') { text('preview-error', error.message); show('preview-error', true); }
    } finally {
      if (state.previewController === controller) { state.previewController = null; show('preview-loading', false); }
    }
  }

  function openDelete(task, trigger) {
    state.deleteTask = task;
    text('delete-description', `确定移除任务「${task.id}」及其自动同步计划吗？`);
    show('delete-error', false);
    openDialog('delete-dialog', trigger);
    $('delete-dialog').querySelector('.modal-footer [data-close]').focus();
  }

  async function deleteTask() {
    const task = state.deleteTask;
    if (!task) return;
    const key = `task:${task.id}:delete`;
    state.busy.add(key);
    show('delete-error', false);
    try {
      await busyButton($('delete-confirm'), '正在删除', async () => {
        $('delete-dialog').setAttribute('aria-busy', 'true');
        await request(`/api/tasks/${encodeURIComponent(task.id)}`, { method: 'DELETE' });
        $('delete-dialog').removeAttribute('aria-busy');
        $('delete-dialog').close();
        await refreshOverview({ force: true });
        toast(`任务 ${task.id} 已移除，本地文件已保留`);
      });
    } catch (error) { text('delete-error', error.message); show('delete-error', true); }
    finally { state.busy.delete(key); $('delete-dialog').removeAttribute('aria-busy'); renderTasks(true); }
  }

  function renderHistory(force = false) {
    if (!state.overview) return;
    const runs = state.overview.runs;
    const ids = [...new Set([...state.overview.tasks.map((task) => String(task.id)), ...runs.map((run) => String(run.task_id))])].sort();
    const optionsSignature = JSON.stringify(ids);
    if (optionsSignature !== state.historyOptionsSignature) {
      const selected = $('history-task-filter').value;
      const first = element('option', '', '全部任务'); first.value = '';
      $('history-task-filter').replaceChildren(first, ...ids.map((id) => { const option = element('option', '', id); option.value = id; return option; }));
      if (ids.includes(selected)) $('history-task-filter').value = selected;
      state.historyOptionsSignature = optionsSignature;
    }
    const task = $('history-task-filter').value;
    const status = $('history-status-filter').value;
    const visible = runs.filter((run) => (!task || run.task_id === task) && (!status || run.status === status));
    text('history-summary', `服务返回最近 ${number(runs.length)} 条记录${task || status ? `，筛选后 ${number(visible.length)} 条` : ''}`);
    show('history-empty', visible.length === 0);
    show('history-table', visible.length > 0);
    const signature = JSON.stringify([visible, task, status]);
    if (!force && signature === state.historySignature) return;
    state.historySignature = signature;
    const expanded = new Set([...$('history-body').querySelectorAll('details[open]')].map((node) => node.dataset.runId));
    rememberFocus($('history-body'), () => {
      const rows = visible.map((run) => {
        const row = element('tr');
        const started = cell('开始时间');
        started.append(element('div', '', date(run.started_at)), element('span', 'cell-subtitle', run.finished_at ? `结束 ${date(run.finished_at, true)}` : '尚未结束'));
        const taskID = cell('任务标识'); taskID.append(element('code', 'mono', run.task_id));
        const statusCell = cell('运行状态'); statusCell.append(statusBadge(run.status));
        const result = cell('同步结果');
        const counts = element('div', 'result-counts');
        counts.append(element('span', '', `下载 ${number(run.downloaded)}`), element('span', '', `跳过 ${number(run.skipped)}`), element('span', '', `失败 ${number(run.failed)}`));
        result.append(counts);
        const detail = cell('详情');
        if (run.error) {
          const disclosure = element('details');
          disclosure.dataset.runId = String(run.id);
          disclosure.open = expanded.has(String(run.id));
          const summary = element('summary', '', '查看错误');
          summary.dataset.focusKey = `history:${run.id}:error`;
          disclosure.append(summary, element('p', '', run.error));
          detail.append(disclosure);
        } else detail.append(element('span', 'cell-subtitle', run.playlist_name || '—'));
        row.append(started, taskID, statusCell, result, detail);
        return row;
      });
      $('history-body').replaceChildren(...rows);
    });
  }

  function renderSettings(force = false) {
    const data = state.overview;
    if (!data) return;
    text('settings-key-status', data.api_key_configured ? '密钥已配置' : '密钥未配置');
    $('settings-key-status').className = `status-badge ${data.api_key_configured ? 'status-success' : 'status-warning'}`;
    text('settings-key-env', data.api_key_env || 'CHKSZ_API_KEY');
    if (state.settingsDirty && !force) return;
    $('settings-timezone').value = data.timezone || '';
    $('settings-schedule').value = data.schedule || '';
    $('settings-quality').value = data.quality || 'lossless';
    $('settings-fallback').checked = Boolean(data.fallback_to_api);
    $('settings-fields').disabled = state.busy.has('settings');
    $('settings-submit').disabled = state.busy.has('settings');
  }

  async function saveSettings(event) {
    event.preventDefault();
    clearErrors('settings');
    const input = { timezone: $('settings-timezone').value.trim(), schedule: $('settings-schedule').value.trim(), quality: $('settings-quality').value, fallback_to_api: $('settings-fallback').checked };
    const errors = [];
    if (!input.timezone) errors.push({ field: 'settings-timezone', message: '请填写 IANA 时区名称。' });
    if (!validCron(input.schedule)) errors.push({ field: 'settings-schedule', message: '默认同步时间需要使用五字段 cron 表达式。' });
    if (errors.length) { formErrors('settings', errors); return; }
    state.busy.add('settings');
    try {
      await busyButton($('settings-submit'), '正在保存', async () => {
        $('settings-fields').disabled = true;
        await request('/api/settings', { method: 'PUT', body: input });
        state.settingsDirty = false;
        await refreshOverview({ force: true });
        renderSettings(true);
        text('settings-dirty', '设置已保存到本机。');
        toast('默认同步规则已更新');
      });
    } catch (error) { formErrors('settings', [{ message: error.message }]); }
    finally {
      state.busy.delete('settings');
      $('settings-fields').disabled = false;
      $('settings-submit').disabled = false;
    }
  }

  async function searchSongs(offset = 0, fresh = false) {
    if (state.search.loading) return;
    const keyword = fresh ? $('search-keyword').value.trim() : state.search.keyword;
    const platform = fresh ? $('search-platform').value : state.search.platform;
    show('search-field-error', false);
    $('search-keyword').removeAttribute('aria-invalid');
    if (!keyword) {
      text('search-field-error', '请输入歌曲、歌手或专辑关键词。');
      show('search-field-error', true);
      $('search-keyword').setAttribute('aria-invalid', 'true');
      $('search-keyword').focus();
      return;
    }
    const controller = new AbortController();
    state.search.controller = controller;
    state.search.loading = true;
    $('search-keyword').disabled = true;
    $('search-platform').disabled = true;
    $('search-prev').disabled = true;
    $('search-next').disabled = true;
    show('search-initial', false);
    show('search-empty', false);
    show('search-error', false);
    show('search-loading', true);
    show('search-results-table', false);
    show('search-pagination', false);
    show('search-source', false);
    try {
      await busyButton($('search-submit'), '正在搜索', async () => {
        const params = new URLSearchParams({ platform, keyword, limit: String(state.search.limit), offset: String(offset) });
        const data = await request(`/api/search?${params.toString()}`, { signal: controller.signal, timeout: 120000 });
        if (!data || !Array.isArray(data.tracks)) throw new Error('搜索服务返回了无法识别的歌曲列表。');
        state.search.keyword = keyword;
        state.search.platform = platform;
        state.search.offset = offset;
        state.search.tracks = data.tracks;
        state.search.loaded = true;
        renderSearchResults(data.fetched_via);
      });
    } catch (error) {
      if (error.name !== 'AbortError') { text('search-error', error.message); show('search-error', true); text('search-results-summary', '检索未完成，请调整关键词或稍后重试。'); }
    } finally {
      state.search.loading = false;
      state.search.controller = null;
      $('search-keyword').disabled = false;
      $('search-platform').disabled = false;
      show('search-loading', false);
      $('search-prev').disabled = state.search.offset === 0;
      $('search-next').disabled = state.search.tracks.length < state.search.limit;
    }
  }

  function renderSearchResults(source) {
    const data = state.search;
    const tracks = data.tracks;
    text('search-results-summary', `${platforms[data.platform]} · “${data.keyword}” · 本页 ${number(tracks.length)} 首`);
    text('search-page-label', `第 ${Math.floor(data.offset / data.limit) + 1} 页`);
    show('search-pagination', true);
    show('search-empty', tracks.length === 0);
    show('search-results-table', tracks.length > 0);
    if (source === 'native' || source === 'chksz_fallback') {
      text('search-source', source === 'native' ? '平台直连' : 'ChKSz 回退');
      show('search-source', true);
    }
    const rows = tracks.map((track) => {
      const row = element('tr');
      const song = cell('歌曲 / 平台标识');
      song.append(element('div', 'cell-title', track.name || '未命名歌曲'), element('code', 'cell-subtitle mono', track.id));
      const artist = cell('歌手'); artist.textContent = artistNames(track);
      const album = cell('专辑'); album.textContent = track.album || '—';
      const length = cell('时长'); length.textContent = duration(track.duration);
      const action = cell('操作');
      const copy = actionButton('', 'copy', 'icon-button copy-button', () => copyID(track.id));
      copy.setAttribute('aria-label', `复制歌曲 ${track.name || track.id} 的平台标识`);
      copy.title = '复制平台标识';
      action.append(copy);
      row.append(song, artist, album, length, action);
      return row;
    });
    $('search-results-body').replaceChildren(...rows);
  }

  async function copyID(id) {
    try {
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(String(id));
      toast('歌曲平台标识已复制');
    } catch { toast('无法自动复制，请手动选择歌曲标识并复制。', true); }
  }

  function route() {
    const requested = window.location.hash.slice(1) || 'overview';
    const next = Object.hasOwn(titles, requested) ? requested : 'overview';
    if (requested !== next) history.replaceState(null, '', `#${next}`);
    document.querySelectorAll('[data-view]').forEach((view) => { view.hidden = view.dataset.view !== next; });
    document.querySelectorAll('[data-nav]').forEach((link) => {
      if (link.dataset.nav === next) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    text('breadcrumb-current', titles[next]);
    document.title = `${titles[next]} · Meloport`;
    const changed = state.route && next !== state.route;
    state.route = next;
    if (changed) { $(`${next}-title`).focus({ preventScroll: true }); window.scrollTo({ top: 0, behavior: 'instant' }); }
  }

  $('refresh-button').addEventListener('click', async (event) => {
    await busyButton(event.currentTarget, '', async () => refreshOverview({ force: true }));
  });
  $('add-task-button').addEventListener('click', (event) => openTask(null, event.currentTarget));
  $('empty-add-task-button').addEventListener('click', (event) => openTask(null, event.currentTarget));
  $('task-filter').addEventListener('input', () => renderTasks());
  $('task-platform').addEventListener('change', () => renderTaskQuality($('task-platform').value));
  $('task-form').addEventListener('submit', saveTask);
  $('delete-confirm').addEventListener('click', deleteTask);
  $('history-task-filter').addEventListener('change', () => renderHistory(true));
  $('history-status-filter').addEventListener('change', () => renderHistory(true));
  $('settings-form').addEventListener('input', () => { state.settingsDirty = true; text('settings-dirty', '有未保存的更改。'); });
  $('settings-form').addEventListener('submit', saveSettings);
  $('search-form').addEventListener('submit', (event) => { event.preventDefault(); searchSongs(0, true); });
  $('search-prev').addEventListener('click', () => searchSongs(Math.max(0, state.search.offset - state.search.limit)));
  $('search-next').addEventListener('click', () => searchSongs(state.search.offset + state.search.limit));
  $('run-all-button').addEventListener('click', async (event) => {
    state.busy.add('run-all');
    try {
      await busyButton(event.currentTarget, '正在排队', async () => {
        await request('/api/run', { method: 'POST' });
        await refreshOverview({ force: true });
        toast('已提交全部可执行任务，请在运行记录中查看结果');
      });
    } catch (error) { toast(error.message, true); }
    finally { state.busy.delete('run-all'); renderOverview(); }
  });
  document.querySelectorAll('[data-close]').forEach((button) => button.addEventListener('click', () => closeDialog(button.dataset.close)));
  document.querySelectorAll('dialog').forEach((dialog) => {
    dialog.addEventListener('cancel', (event) => { if (dialog.getAttribute('aria-busy') === 'true') event.preventDefault(); });
    dialog.addEventListener('close', () => {
      if (dialog.id === 'preview-dialog' && state.previewController) { state.previewController.abort(); state.previewController = null; }
      const trigger = state.dialogTriggers.get(dialog.id);
      if (trigger && trigger.isConnected) trigger.focus({ preventScroll: true });
    });
  });
  document.addEventListener('visibilitychange', () => {
    clearTimeout(state.pollTimer);
    if (!document.hidden) refreshOverview({ quiet: true, force: true });
  });
  window.addEventListener('hashchange', route);
  window.addEventListener('pagehide', () => {
    clearTimeout(state.pollTimer);
    if (state.overviewController) state.overviewController.abort();
    if (state.previewController) state.previewController.abort();
    if (state.search.controller) state.search.controller.abort();
  });
  route();
  refreshOverview();
})();
