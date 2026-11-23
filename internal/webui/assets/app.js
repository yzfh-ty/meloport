(() => {
  'use strict';

  const preferenceKey = 'meloport.theme';
  const root = document.documentElement;
  const themes = {
    system: { label: '跟随系统', icon: 'monitor', next: 'light' },
    light: { label: '浅色', icon: 'sun', next: 'dark' },
    dark: { label: '深色', icon: 'moon', next: 'system' }
  };
  let theme = 'system';
  try {
    const saved = localStorage.getItem(preferenceKey);
    if (saved === 'light' || saved === 'dark') theme = saved;
  } catch { /* Fall back to the system appearance. */ }

  function applyTheme() {
    if (theme === 'light' || theme === 'dark') root.dataset.theme = theme;
    else delete root.dataset.theme;
  }

  // Apply the saved override before the stylesheet renders.
  applyTheme();
  document.addEventListener('DOMContentLoaded', () => {
    const button = document.getElementById('theme-toggle');
    const icon = document.getElementById('theme-icon');

    function updateControl() {
      const current = themes[theme];
      icon.setAttribute('href', `#icon-${current.icon}`);
      const description = `当前主题：${current.label}；点击切换为${themes[current.next].label}`;
      button.setAttribute('aria-label', description);
      button.title = description;
    }

    updateControl();
    button.addEventListener('click', () => {
      theme = themes[theme].next;
      applyTheme();
      updateControl();
      try { localStorage.setItem(preferenceKey, theme); } catch {}
    });
  }, { once: true });
})();

// Schedule helpers are kept pure so existing plans can be tested without a browser.
const scheduleUI = (() => {
  const hours = ['1', '2', '3', '4', '6', '8', '12'];
  const minutes = ['5', '10', '15', '20', '30'];
  const weekdays = ['日', '一', '二', '三', '四', '五', '六'];
  const integer = (value, max) => /^\d{1,2}$/.test(value) && Number(value) <= max;
  const clock = (hour, minute) => `${String(Number(hour)).padStart(2, '0')}:${String(Number(minute)).padStart(2, '0')}`;

  function decode(value, allowInherit = false) {
    const original = String(value || '');
    const result = { mode: 'custom', time: '03:00', weekday: '1', interval: '2', original };
    const fields = original.trim().split(/\s+/);
    if (!original.trim() && allowInherit) return { ...result, mode: 'inherit' };
    if (fields.length !== 5 || fields[2] !== '*' || fields[3] !== '*') return result;
    const [minute, hour, , , day] = fields;
    if (integer(minute, 59) && integer(hour, 23)) {
      result.time = clock(hour, minute);
      if (day === '*') return { ...result, mode: 'daily' };
      if (day === '1-5' || day.toUpperCase() === 'MON-FRI') return { ...result, mode: 'weekdays' };
      if (/^[0-7]$/.test(day)) return { ...result, mode: 'weekly', weekday: String(Number(day) % 7) };
      const named = ['SUN', 'MON', 'TUE', 'WED', 'THU', 'FRI', 'SAT'].indexOf(day.toUpperCase());
      if (named !== -1) return { ...result, mode: 'weekly', weekday: String(named) };
    }
    if (day !== '*') return result;
    if (minute === '0' && (hour === '*' || /^\*\/\d+$/.test(hour))) {
      const interval = hour === '*' ? '1' : hour.slice(2);
      if (hours.includes(interval)) return { ...result, mode: 'hours', interval };
    }
    if (hour === '*' && /^\*\/\d+$/.test(minute) && minutes.includes(minute.slice(2))) return { ...result, mode: 'minutes', interval: minute.slice(2) };
    return result;
  }

  function encode(model) {
    if (model.mode === 'inherit') return '';
    if (model.mode === 'custom' && String(model.original || '').trim()) return model.original;
    if (model.mode === 'hours' && hours.includes(String(model.interval))) return `0 */${model.interval} * * *`;
    if (model.mode === 'minutes' && minutes.includes(String(model.interval))) return `*/${model.interval} * * * *`;
    if (['daily', 'weekdays', 'weekly'].includes(model.mode)) {
      if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(model.time || '')) throw new Error('请选择有效的同步时间。');
      const [hour, minute] = model.time.split(':').map(Number);
      if (model.mode === 'weekly' && !/^[0-6]$/.test(String(model.weekday))) throw new Error('请选择每周同步的星期。');
      const day = model.mode === 'daily' ? '*' : model.mode === 'weekdays' ? '1-5' : model.weekday;
      return `${minute} ${hour} * * ${day}`;
    }
    throw new Error('请选择有效的同步计划。');
  }

  function describe(value, allowInherit = false) {
    const model = decode(value, allowInherit);
    if (model.mode === 'daily') return `每天 ${model.time}`;
    if (model.mode === 'weekdays') return `周一至周五 ${model.time}`;
    if (model.mode === 'weekly') return `每周${weekdays[Number(model.weekday)]} ${model.time}`;
    if (model.mode === 'hours') return `每 ${model.interval} 小时（整点）`;
    if (model.mode === 'minutes') return `每 ${model.interval} 分钟`;
    if (model.mode === 'inherit') return '继承默认计划';
    return '自定义计划';
  }

  return { decode, encode, describe, hours, minutes };
})();
// End schedule helpers.

document.addEventListener('DOMContentLoaded', () => {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const platforms = { netease: '网易云音乐', qq: 'QQ 音乐', kugou: '酷狗音乐' };
  const titles = { overview: '任务概览', audio: '音频文件', cloud115: '115 与 STRM', navidrome: 'Navidrome', search: '歌曲检索', rankings: '排行榜', history: '运行记录', settings: '系统设置' };
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
  const audioFields = { title: '歌名', artist: '歌手', album: '专辑', cover: '封面', lyrics: '歌词' };
  const audioStatus = {
    ready: ['元数据齐全', 'success'], incomplete: ['有缺失项', 'warning'],
    missing: ['找不到文件', 'error'], changed: ['文件已改动', 'warning'], outside: ['位于旧目录', 'neutral'],
    unsupported: ['格式不支持', 'neutral'], unreadable: ['无法读取标签', 'error'], archived: ['任务已删除', 'neutral']
  };
  const state = {
    authenticated: false,
    overview: null, route: '', editTask: null, deleteTask: null,
    taskSignature: '', historySignature: '', historyOptionsSignature: '', settingsDirty: false,
    busy: new Set(), overviewVersion: 0, overviewController: null, overviewPromise: null,
    previewController: null, rankingDetail: null, pollTimer: null, toastTimer: null, dialogTriggers: new Map(),
    search: { keyword: '', platform: 'netease', offset: 0, limit: 20, tracks: [], loaded: false, loading: false, controller: null },
    rankings: { platform: 'netease', catalog: {}, loading: false, error: '', filter: '', category: '', controller: null },
    audio: { filters: {}, offsets: [0], nextOffset: null, items: [], loading: false, error: '', controller: null, repairing: null },
    cloud: { data: null, dirty: false, page: 0, controller: null, operation: '', login: null, picker: null, directorySelection: null },
    navidrome: { dirty: false },
    schedules: {}
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
      if (response.status === 401 && path !== '/api/session' && state.authenticated) lockSession('登录已过期，请重新登录。');
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
    if (state.authenticated && !document.hidden) state.pollTimer = setTimeout(() => refreshOverview({ quiet: true }), 5000);
  }

  function refreshOverview({ quiet = false, force = false } = {}) {
    if (!state.authenticated) return Promise.resolve();
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
        text('connection-label', '已连接');
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
    text('task-count', data.tasks.length);
    text('task-timezone', `默认：${scheduleUI.describe(data.schedule)} · ${data.timezone}`);
    $('add-task-button').disabled = false;
    const queue = data.queue || {};
    const running = Array.isArray(queue.running) ? queue.running.length : 0;
    const pending = Array.isArray(queue.pending) ? queue.pending.length : 0;
    text('queue-summary', running || pending ? `正在同步 ${running} 项 · 排队 ${pending} 项` : '同步队列空闲');
    show('key-notice', !data.api_key_configured);
    text('key-notice-text', `平台检索和歌单预览仍可使用。下载解析前，请配置本地 api.key 或环境变量 ${data.api_key_env || 'CHKSZ_API_KEY'}，然后重启服务。`);
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
    const sourceType = task.source_type || 'playlist';
    return { id: String(task.id), platform: task.platform || 'netease', source_type: sourceType, source_id: sourceType === 'ranking' ? task.source_id || '' : '', playlist: task.playlist || '', schedule: task.schedule || '', quality: task.quality || '', auto_downgrade: task.auto_downgrade ?? null, directory: task.directory || '', enabled: Boolean(task.enabled) };
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
    const visible = tasks.filter((task) => `${task.id} ${task.playlist || ''} ${task.source_id || ''} ${platforms[task.platform] || ''}`.toLocaleLowerCase().includes(filter));
    const signature = JSON.stringify([visible, filter, [...state.busy], state.overview.schedule, state.overview.api_key_configured, state.overview.queue && state.overview.queue.paused]);
    show('tasks-loading', false);
    show('tasks-empty', tasks.length === 0);
    show('tasks-no-match', tasks.length > 0 && visible.length === 0);
    show('tasks-table', visible.length > 0);
    if (!force && signature === state.taskSignature) return;
    state.taskSignature = signature;
    rememberFocus($('tasks-body'), () => {
      const rows = visible.map((task) => {
        const row = element('tr');
        const identity = cell('任务 / 来源');
        const identityLayout = element('div', 'task-identity');
        const artwork = element('span', 'playlist-icon');
        artwork.dataset.platform = task.platform;
        artwork.append(icon('music'));
        const name = element('div', 'task-name');
        const sourceLabel = task.source_type === 'ranking' ? `排行榜 · ${task.source_id || '—'}` : (task.playlist || '—');
        const playlist = element('span', 'cell-subtitle playlist-value', sourceLabel);
        playlist.title = task.playlist;
        name.append(element('div', 'cell-title', task.id), playlist);
        identityLayout.append(artwork, name);
        identity.append(identityLayout);
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
        schedule.append(element('span', '', scheduleUI.describe(task.effective_schedule || task.schedule || state.overview.schedule)), element('span', 'cell-subtitle', task.schedule ? '独立计划' : '继承默认'));
        const latest = cell('最近运行');
        latest.append(statusBadge(task.running ? 'running' : task.queued ? 'queued' : task.last_run ? task.last_run.status : ''));
        if (task.last_run) latest.append(element('span', 'cell-subtitle', date(task.last_run.started_at, true)));
        const next = cell('下次执行');
        next.append(element('span', '', task.enabled ? date(task.next_run, true) : '—'));
        const quality = task.effective_quality || task.quality || state.overview.quality;
        next.append(element('span', 'cell-subtitle', quality ? `音质 ${quality}` : '继承全局音质'));
        const management = cell('管理任务');
        const actions = element('div', 'task-actions');
        const run = actionButton(task.running ? '同步中' : task.queued ? '已排队' : '同步', 'play', 'button button-small button-secondary task-run', (event) => runTask(task, event.currentTarget));
        run.dataset.focusKey = `${task.id}:run`;
        run.disabled = !task.enabled || task.running || task.queued || Boolean(state.overview.queue && state.overview.queue.paused) || state.busy.has(`task:${task.id}:run`);
        run.title = !task.enabled ? '请先启用任务' : !state.overview.api_key_configured ? '未配置解析密钥时，缺失歌曲无法下载' : '';
        const preview = actionButton('', 'eye', 'icon-button', (event) => previewTask(task, event.currentTarget));
        preview.setAttribute('aria-label', `预览来源 ${task.id}`);
        preview.title = '预览来源';
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
    text('task-source-id-hint', `${platforms[platform] || '当前平台'}排行榜 ID 来自排行榜页面，平台之间不能混用。`);
  }

  function renderTaskSource() {
    const ranking = $('task-source-type').value === 'ranking';
    show('task-playlist-field', !ranking);
    show('task-ranking-field', ranking);
    $('task-playlist').required = !ranking;
    $('task-source-id').required = ranking;
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

  function scheduleModel(prefix) {
    return { mode: $(`${prefix}-schedule`).value, time: $(`${prefix}-schedule-time`).value, weekday: $(`${prefix}-schedule-weekday`).value, interval: $(`${prefix}-schedule-interval`).value, original: state.schedules[prefix].original };
  }

  function readSchedule(prefix) {
    const saved = state.schedules[prefix];
    if (!saved.changed && (saved.original || prefix === 'task')) return saved.original;
    return scheduleUI.encode(scheduleModel(prefix));
  }

  function updateScheduleControls(prefix) {
    const mode = $(`${prefix}-schedule`).value;
    show(`${prefix}-schedule-time-field`, ['daily', 'weekdays', 'weekly'].includes(mode));
    show(`${prefix}-schedule-weekday-field`, mode === 'weekly');
    show(`${prefix}-schedule-interval-field`, mode === 'hours' || mode === 'minutes');
    const interval = $(`${prefix}-schedule-interval`);
    const values = mode === 'minutes' ? scheduleUI.minutes : scheduleUI.hours;
    const previous = interval.value;
    interval.replaceChildren(...values.map((value) => { const option = element('option', '', `${value} ${mode === 'minutes' ? '分钟' : '小时'}`); option.value = value; return option; }));
    interval.value = values.includes(previous) ? previous : values[0];
    text(`${prefix}-schedule-interval-label`, mode === 'minutes' ? '分钟间隔' : '小时间隔');
    if (mode === 'custom') {
      text(`${prefix}-schedule-hint`, '已有自定义计划，保持不变。选择上方其他计划后才会替换。');
    } else if (mode === 'inherit') {
      text(`${prefix}-schedule-hint`, `继承默认：${state.overview ? scheduleUI.describe(state.overview.schedule) : '读取后显示'}。`);
    } else {
      try {
        const description = scheduleUI.describe(scheduleUI.encode(scheduleModel(prefix)));
        text(`${prefix}-schedule-hint`, `${description}，按${prefix === 'settings' ? '设置的' : '系统设置中的'}时区运行。${mode === 'hours' ? '从每天 00:00 起计算。' : mode === 'minutes' ? '从每小时第 0 分钟起计算。' : ''}`);
      } catch { text(`${prefix}-schedule-hint`, '请选择完整的同步计划。'); }
    }
  }

  function setSchedule(prefix, value) {
    const model = scheduleUI.decode(value, prefix === 'task');
    state.schedules[prefix] = { original: String(value || ''), changed: false };
    const custom = $(`${prefix}-schedule`).querySelector('[value="custom"]');
    custom.hidden = model.mode !== 'custom';
    $(`${prefix}-schedule`).value = model.mode;
    $(`${prefix}-schedule-time`).value = model.time;
    $(`${prefix}-schedule-weekday`).value = model.weekday;
    updateScheduleControls(prefix);
    if (['hours', 'minutes'].includes(model.mode)) $(`${prefix}-schedule-interval`).value = model.interval;
    updateScheduleControls(prefix);
  }

  function setupSchedule(prefix) {
    const select = $(`${prefix}-schedule`);
    const options = [...(prefix === 'task' ? [['inherit', '继承默认计划']] : []), ['daily', '每天'], ['weekdays', '工作日（周一至周五）'], ['weekly', '每周'], ['hours', '每隔几小时'], ['minutes', '每隔几分钟'], ['custom', '保留自定义计划']];
    select.replaceChildren(...options.map(([value, label]) => { const option = element('option', '', label); option.value = value; return option; }));
    const days = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
    $(`${prefix}-schedule-weekday`).replaceChildren(...days.map((label, value) => { const option = element('option', '', label); option.value = String(value); return option; }));
    const editor = $(`${prefix}-schedule-editor`);
    editor.addEventListener('input', () => { state.schedules[prefix].changed = true; });
    editor.addEventListener('change', () => {
      state.schedules[prefix].changed = true;
      updateScheduleControls(prefix);
      if (prefix === 'settings') markSettingsDirty();
    });
    setSchedule(prefix, prefix === 'task' ? '' : '0 3 * * *');
  }

  function openTask(task, trigger, preset = null) {
    state.editTask = task;
    clearErrors('task');
    $('task-form').reset();
    const platform = task ? task.platform : preset ? preset.platform : 'netease';
    const sourceType = task ? task.source_type || 'playlist' : preset ? preset.sourceType : 'playlist';
    let taskID = task ? task.id : '';
    if (preset && !task) {
      const baseID = `rank-${preset.platform}-${preset.sourceId}`;
      taskID = baseID;
      let suffix = 2;
      while (state.overview && state.overview.tasks.some((item) => String(item.id).toLowerCase() === taskID.toLowerCase())) taskID = `${baseID}-${suffix++}`;
    }
    $('task-id').value = taskID;
    $('task-id').readOnly = Boolean(task);
    $('task-platform').value = platform;
    $('task-source-type').value = sourceType;
    $('task-playlist').value = task ? task.playlist || '' : '';
    $('task-source-id').value = task ? task.source_id || '' : preset ? preset.sourceId : '';
    setSchedule('task', task ? task.schedule || '' : '');
    $('task-directory').value = task ? task.directory || '' : '';
    $('task-auto-downgrade').value = task?.auto_downgrade == null ? '' : String(Boolean(task.auto_downgrade));
    $('task-enabled').checked = task ? Boolean(task.enabled) : true;
    $('task-form').querySelector('.advanced-fields').open = Boolean(task && (task.schedule || task.quality || task.directory));
    renderTaskQuality($('task-platform').value, task ? task.quality || '' : '');
    renderTaskSource();
    text('task-dialog-title', task ? '编辑订阅任务' : '添加订阅');
    updateTaskSubmit();
    openDialog('task-dialog', trigger);
    (task ? (sourceType === 'ranking' ? $('task-source-id') : $('task-playlist')) : preset ? $('task-id') : $('task-id')).focus();
  }

  function updateTaskSubmit() {
    const enabled = $('task-enabled').checked;
    text('task-submit', state.editTask ? '保存修改' : enabled ? '添加并开始下载' : '添加订阅');
    text('task-enabled-hint', !enabled ? '暂时只保存订阅，不自动下载。' : state.editTask ? '后续按计划同步；保存修改不会立即下载。' : '添加后立即下载缺少的歌曲，后续按计划同步。');
  }

  function taskSavedMessage(input, result, editing) {
    if (editing) return `订阅 ${input.id} 已更新`;
    if (result.queued) return `订阅 ${input.id} 已添加，首次下载已排队`;
    if (!input.enabled) return `订阅 ${input.id} 已添加，自动同步未启用`;
    if (result.queue_reason === 'paused') return `订阅 ${input.id} 已添加；下载队列暂停，恢复后可点击“同步”`;
    return `订阅 ${input.id} 已添加；队列已满，可稍后点击“同步”`;
  }

  async function saveTask(event) {
    event.preventDefault();
    if ($('task-submit').disabled) return;
    clearErrors('task');
    const input = {
      id: $('task-id').value.trim(), platform: $('task-platform').value, source_type: $('task-source-type').value,
      source_id: $('task-source-id').value.trim(), playlist: $('task-playlist').value.trim(),
      schedule: '', quality: $('task-quality').value,
      auto_downgrade: $('task-auto-downgrade').value === '' ? null : $('task-auto-downgrade').value === 'true',
      directory: $('task-directory').value.trim(), enabled: $('task-enabled').checked
    };
    const errors = [];
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(input.id) || /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i.test(input.id)) errors.push({ field: 'task-id', message: '任务标识需为 1–64 位字母、数字、下划线或短横线，并避免系统保留名称。' });
    if (!state.editTask && state.overview && state.overview.tasks.some((task) => String(task.id).toLowerCase() === input.id.toLowerCase())) errors.push({ field: 'task-id', message: '这个任务标识已经存在，请使用其他标识。' });
    if (input.source_type === 'ranking') {
      if (!/^[1-9][0-9]{0,19}$/.test(input.source_id)) errors.push({ field: 'task-source-id', message: '请输入有效的正整数排行榜 ID。' });
      input.playlist = '';
    } else {
      input.source_id = '';
      if (!input.playlist) errors.push({ field: 'task-playlist', message: '请填写歌单链接、ID 或分享码。' });
    }
    try { input.schedule = readSchedule('task'); } catch (error) { errors.push({ field: 'task-schedule', message: error.message }); }
    if (errors.length) { formErrors('task', errors); return; }
    const editing = state.editTask;
    try {
      await busyButton($('task-submit'), '正在保存', async () => {
        $('task-fields').disabled = true;
        $('task-dialog').setAttribute('aria-busy', 'true');
        const path = editing ? `/api/tasks/${encodeURIComponent(editing.id)}` : '/api/tasks';
        const result = await request(path, { method: editing ? 'PUT' : 'POST', body: input });
        $('task-dialog').removeAttribute('aria-busy');
        $('task-dialog').close();
        await refreshOverview({ force: true });
        toast(taskSavedMessage(input, result, editing));
      });
    } catch (error) { formErrors('task', [{ message: error.message }]); }
    finally { $('task-fields').disabled = false; $('task-dialog').removeAttribute('aria-busy'); }
  }

  async function previewTask(task, trigger) {
    if (state.previewController) state.previewController.abort();
    const controller = new AbortController();
    state.previewController = controller;
    state.rankingDetail = null;
    show('preview-subscribe', false);
    text('preview-title', `${task.source_type === 'ranking' ? '排行榜预览' : '歌单预览'} · ${task.id}`);
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
      text('preview-title', playlist.name || `${task.source_type === 'ranking' ? '排行榜预览' : '歌单预览'} · ${task.id}`);
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
    const filteredEmpty = runs.length > 0 && Boolean(task || status);
    text('history-empty-title', filteredEmpty ? '没有符合筛选条件的记录' : '还没有运行记录');
    text('history-empty-description', filteredEmpty ? '请调整任务或状态筛选条件后重试。' : '执行一次同步后，会在这里看到结果。');
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

  function markSettingsDirty() {
    state.settingsDirty = true;
    text('settings-dirty', '有未保存的更改。');
    const saved = state.overview;
    const directoryChanged = saved && $('settings-music-dir').value.trim() !== (saved.music_dir || '');
    text('settings-music-dir-hint', directoryChanged ? '目录已修改，保存后用于后续同步。' : '填写服务端保存路径；已有音频会保留。');
  }

  function renderSettings(force = false) {
    const data = state.overview;
    if (!data) return;
    text('settings-key-status', data.api_key_configured ? '解析密钥已配置' : '解析密钥未配置');
    $('settings-key-status').className = `status-badge ${data.api_key_configured ? 'status-success' : 'status-warning'}`;
    if (state.settingsDirty && !force) return;
    $('settings-timezone').value = data.timezone || '';
    setSchedule('settings', data.schedule || '');
    $('settings-music-dir').value = data.music_dir || '';
    text('settings-music-dir-hint', '填写服务端保存路径；已有音频会保留。');
    $('settings-quality').value = data.quality || 'lossless';
    $('settings-auto-downgrade').checked = Boolean(data.auto_downgrade);
    $('settings-fallback').checked = Boolean(data.fallback_to_api);
    $('settings-fields').disabled = state.busy.has('settings');
    $('settings-submit').disabled = state.busy.has('settings');
  }

  async function saveSettings(event) {
    event.preventDefault();
    if (state.busy.has('settings')) return;
    clearErrors('settings');
    const input = { timezone: $('settings-timezone').value.trim(), schedule: '', music_dir: $('settings-music-dir').value.trim(), quality: $('settings-quality').value, auto_downgrade: $('settings-auto-downgrade').checked, fallback_to_api: $('settings-fallback').checked };
    const errors = [];
    if (!input.timezone) errors.push({ field: 'settings-timezone', message: '请填写 IANA 时区名称。' });
    if (!input.music_dir) errors.push({ field: 'settings-music-dir', message: '请填写下载根目录。' });
    try { input.schedule = readSchedule('settings'); } catch (error) { errors.push({ field: 'settings-schedule', message: error.message }); }
    if (errors.length) { formErrors('settings', errors); return; }
    state.busy.add('settings');
    try {
      await busyButton($('settings-submit'), '正在保存', async () => {
        $('settings-fields').disabled = true;
        await request('/api/settings', { method: 'PUT', body: input });
        const previous = state.overview;
        if (previous) state.overview = { ...previous, ...input };
        state.settingsDirty = false;
        await refreshOverview({ force: true });
        renderSettings(true);
        text('settings-dirty', '设置已保存。');
        toast('下载目录与默认规则已更新');
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
    text('search-results-summary', `${platforms[platform]} · “${keyword}” · 正在检索…`);
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
      text('search-results-summary', `${platforms[platform]} · “${keyword}” · ${error.name === 'AbortError' ? '检索已取消。' : '检索未完成，请调整关键词或稍后重试。'}`);
      if (error.name !== 'AbortError') { text('search-error', error.message); show('search-error', true); }
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

  function audioMissing(item) {
    return Array.isArray(item.missing) ? item.missing.filter((key) => Object.hasOwn(audioFields, key)).map((key) => audioFields[key]) : [];
  }

  function audioName(item) {
    return item.title || String(item.path || '').split(/[\\/]/).pop() || item.track_id;
  }

  function renderAudio() {
    const data = state.audio;
    const resultsVisible = !data.loading && !data.error;
    show('audio-loading', data.loading);
    text('audio-error', data.error);
    show('audio-error', Boolean(data.error));
    show('audio-table', resultsVisible && data.items.length > 0);
    show('audio-empty', resultsVisible && data.items.length === 0);
    show('audio-pagination', resultsVisible && (data.items.length > 0 || data.offsets.length > 1 || data.nextOffset !== null));
    $('audio-prev').disabled = data.loading || data.offsets.length === 1;
    $('audio-next').disabled = data.loading || data.nextOffset === null;
    $('audio-refresh').disabled = data.loading;
    text('audio-page-label', `第 ${number(data.offsets.length)} 页 · 本页 ${number(data.items.length)} 个文件`);

    const filtered = Object.values(data.filters).some(Boolean);
    if (resultsVisible && data.items.length === 0) {
      text('audio-empty-title', filtered ? '没有匹配的文件' : '还没有登记的音频文件');
      text('audio-empty-detail', data.nextOffset !== null ? '本页未查到匹配项，可以继续查看下一页。' : filtered ? '试试修改筛选条件。' : '歌曲下载完成后会显示在这里。');
    }

    const rows = data.items.map((item) => {
      const row = element('tr');
      const song = cell('音频文件');
      song.append(element('div', 'cell-title', audioName(item)));
      const details = [artistNames(item), item.album].filter((value) => value && value !== '—');
      if (details.length) song.append(element('span', 'cell-subtitle', details.join(' · ')));
      if (item.path) song.append(element('span', 'cell-subtitle audio-path', item.path));

      const source = cell('来源');
      source.append(platformBadge(item.provider), element('span', 'cell-subtitle', item.task_id || '—'));
      if (Number.isFinite(item.size) && item.size > 0) source.append(element('span', 'cell-subtitle', bytes(item.size)));

      const metadata = cell('文件内元数据');
      const missing = audioMissing(item);
      if (missing.length) {
        const badges = element('div', 'audio-missing');
        badges.append(...missing.map((label) => element('span', 'audio-missing-tag', `缺少${label}`)));
        metadata.append(badges);
      } else {
        const info = Object.hasOwn(audioStatus, item.status) ? audioStatus[item.status] : ['未检查', 'neutral'];
        metadata.append(element('span', `status-badge status-${info[1]}`, info[0]));
      }

      const actions = cell('操作');
      actions.className = 'audio-actions';
      if (item.retryable) {
        const key = JSON.stringify([item.task_id, item.provider, item.track_id]);
        const active = data.repairing === key;
        const retry = actionButton(active ? '正在补全' : '重试补全', active ? null : 'refresh', 'button button-small button-secondary', (event) => retryAudio(item, key, event.currentTarget));
        retry.dataset.focusKey = key;
        retry.setAttribute('aria-label', `重试补全《${audioName(item)}》的元数据`);
        retry.disabled = data.repairing !== null;
        if (active) {
          retry.classList.add('is-loading');
          retry.setAttribute('aria-busy', 'true');
        }
        actions.append(retry);
      } else {
        actions.append(element('span', 'audio-no-action', '—'));
      }
      row.append(song, source, metadata, actions);
      return row;
    });
    const body = $('audio-body');
    rememberFocus(body, () => body.replaceChildren(...rows));
  }

  async function loadAudio(offsets = state.audio.offsets) {
    const data = state.audio;
    if (data.controller) data.controller.abort();
    const controller = new AbortController();
    data.controller = controller;
    data.loading = true;
    data.error = '';
    const offset = offsets[offsets.length - 1];
    renderAudio();
    try {
      const params = new URLSearchParams({ ...data.filters, offset: String(offset), limit: '30' });
      const response = await request(`/api/audio?${params}`, { signal: controller.signal, timeout: 30000 });
      if (controller.signal.aborted) return;
      if (!response || !Array.isArray(response.items)) throw new Error('服务返回了无法识别的音频文件列表。');
      data.items = response.items;
      data.offsets = offsets;
      data.nextOffset = response.has_more && Number.isSafeInteger(response.next_offset) && response.next_offset > offset ? response.next_offset : null;
      if (Array.isArray(response.tasks)) {
        const select = $('audio-task');
        const selected = select.value;
        const tasks = [...new Set([...response.tasks, selected].filter(Boolean))];
        select.replaceChildren(element('option', '', '全部任务'), ...tasks.map((task) => {
          const option = element('option', '', task);
          option.value = task;
          return option;
        }));
        select.firstElementChild.value = '';
        select.value = selected;
      }
    } catch (error) {
      if (!controller.signal.aborted) data.error = error.message;
    } finally {
      if (data.controller === controller) {
        data.controller = null;
        data.loading = false;
        renderAudio();
      }
    }
  }

  function showAudioRepair(message, error = false) {
    const result = $('audio-repair-result');
    result.classList.toggle('notice-info', !error);
    result.classList.toggle('notice-error', error);
    result.textContent = message;
    result.hidden = !message;
  }

  async function retryAudio(item, key, button) {
    const data = state.audio;
    if (data.repairing !== null) return;
    data.repairing = key;
    const restoreFocus = document.activeElement === button;
    $('audio-body').querySelectorAll('button').forEach((retry) => { retry.disabled = true; });
    button.classList.add('is-loading');
    button.setAttribute('aria-busy', 'true');
    button.textContent = '正在补全';
    showAudioRepair('');
    const name = `《${audioName(item)}》`;
    try {
      const path = [item.task_id, item.provider, item.track_id].map(encodeURIComponent).join('/');
      const result = await request(`/api/audio/${path}/retag`, { method: 'POST', timeout: 115000 });
      if (!result || !result.item) throw new Error('服务没有返回补全结果，请刷新列表查看。');
      const remaining = audioMissing(result.item);
      const warnings = Array.isArray(result.warnings) ? result.warnings.filter(Boolean) : [];
      let message = `${name}的元数据已补齐。`;
      if (remaining.length) message = `${name}${result.changed ? '已更新，仍缺少' : '未补齐以下项目'}${result.changed ? '' : '：'}${remaining.join('、')}。`;
      else if (result.item.status !== 'ready') message = `${name}已重试，请查看文件状态。`;
      if (warnings.length) message += ` ${warnings.join(' · ')}`;
      showAudioRepair(message);
      if (state.route === 'audio') await loadAudio();
    } catch (error) {
      showAudioRepair(`${name}补全失败。${error.message}`, true);
    } finally {
      data.repairing = null;
      renderAudio();
      if (restoreFocus && state.route === 'audio' && document.activeElement === document.body) {
        const replacement = [...$('audio-body').querySelectorAll('button')].find((retry) => retry.dataset.focusKey === key);
        (replacement || $('audio-query')).focus({ preventScroll: true });
      }
    }
  }

  function rankingItems() {
    const data = state.rankings;
    const catalog = data.catalog[data.platform];
    return catalog && Array.isArray(catalog.items) ? catalog.items : [];
  }

  function renderRankings() {
    const data = state.rankings;
    const items = rankingItems();
    const filter = data.filter.trim().toLocaleLowerCase();
    const visible = items.filter((item) => {
      const haystack = `${item.name || ''} ${item.category || ''} ${item.id || ''}`.toLocaleLowerCase();
      return (!filter || haystack.includes(filter)) && (!data.category || item.category === data.category);
    });
    const categories = [...new Set(items.map((item) => item.category).filter(Boolean))].sort((a, b) => a.localeCompare(b, 'zh-CN'));
    const categorySelect = $('rankings-category');
    const selected = data.category;
    categorySelect.replaceChildren(element('option', '', '全部分类'));
    categorySelect.firstElementChild.value = '';
    categories.forEach((category) => { const option = element('option', '', category); option.value = category; categorySelect.append(option); });
    categorySelect.value = categories.includes(selected) ? selected : '';
    data.category = categorySelect.value;
    show('rankings-loading', data.loading);
    show('rankings-error', Boolean(data.error));
    text('rankings-error-message', data.error || '');
    show('rankings-no-data', !data.loading && !data.error && !data.catalog[data.platform]);
    show('rankings-empty', !data.loading && !data.error && Boolean(data.catalog[data.platform]) && visible.length === 0);
    show('rankings-table', !data.loading && !data.error && visible.length > 0);
    if (!data.loading && !data.error && data.catalog[data.platform] && items.length === 0) {
      $('rankings-empty').querySelector('h3').textContent = filter || data.category ? '没有匹配的榜单' : '当前平台暂无公开榜单';
      $('rankings-empty').querySelector('p').textContent = filter || data.category ? '尝试修改搜索关键词或分类。' : '平台没有返回可订阅的榜单。';
    } else if (!data.loading && !data.error && data.catalog[data.platform] && visible.length === 0) {
      $('rankings-empty').querySelector('h3').textContent = '没有匹配的榜单';
      $('rankings-empty').querySelector('p').textContent = '尝试修改搜索关键词或分类。';
    }
    text('rankings-summary', data.catalog[data.platform] ? `共 ${number(items.length)} 个榜单${visible.length !== items.length ? `，筛选后 ${number(visible.length)} 个` : ''}` : '等待读取榜单');
    const fetchedAt = data.catalog[data.platform] && data.catalog[data.platform].fetchedAt;
    text('rankings-fetched-at', fetchedAt ? `更新于 ${date(fetchedAt, true)}` : '—');
    document.querySelectorAll('#rankings-platform [data-platform]').forEach((button) => {
      const active = button.dataset.platform === data.platform;
      button.setAttribute('aria-selected', String(active));
    });
    const rows = visible.map((item) => {
      const row = element('tr');
      const name = cell('榜单');
      const layout = element('div', 'ranking-identity');
      const artwork = element('span', 'playlist-icon'); artwork.dataset.platform = data.platform; artwork.append(icon('list'));
      const copy = element('div', 'task-name'); copy.append(element('div', 'cell-title', item.name || item.id));
      layout.append(artwork, copy); name.append(layout);
      const count = cell('歌曲数'); count.textContent = Number.isFinite(item.track_count) ? number(item.track_count) : '—';
      const frequency = cell('更新频率'); frequency.textContent = item.update_frequency || '—';
      const action = cell('操作');
      const actions = element('div', 'ranking-row-actions');
      const details = actionButton('详情', 'eye', 'button button-small button-secondary', (event) => openRankingDetails(item, event.currentTarget));
      const subscribe = actionButton('订阅', 'plus', 'button button-small button-primary', (event) => subscribeRanking(item, event.currentTarget));
      actions.append(details, subscribe); action.append(actions); row.append(name, count, frequency, action);
      return row;
    });
    $('rankings-body').replaceChildren(...rows);
  }

  async function loadRankings(platform = state.rankings.platform, force = false) {
    state.rankings.platform = platform;
    state.rankings.error = '';
    const cached = state.rankings.catalog[platform];
    if (cached && !force) {
      if (state.rankings.controller) state.rankings.controller.abort();
      state.rankings.controller = null;
      state.rankings.loading = false;
      renderRankings();
      return;
    }
    if (state.rankings.controller) state.rankings.controller.abort();
    const controller = new AbortController();
    state.rankings.controller = controller;
    state.rankings.loading = true;
    state.rankings.error = '';
    renderRankings();
    try {
      const query = new URLSearchParams({ platform });
      if (force) query.set('refresh', '1');
      const response = await request(`/api/rankings?${query.toString()}`, { signal: controller.signal, timeout: 120000 });
      if (!response || !Array.isArray(response.rankings)) throw new Error('排行榜服务返回了无法识别的目录。');
      state.rankings.catalog[platform] = { items: response.rankings, fetchedAt: response.fetched_at };
    } catch (error) {
      if (error.name !== 'AbortError') state.rankings.error = error.message;
    } finally {
      if (state.rankings.controller === controller) {
        state.rankings.controller = null;
        state.rankings.loading = false;
        renderRankings();
      }
    }
  }

  function subscribeRanking(item, trigger) {
    openTask(null, trigger, { platform: state.rankings.platform, sourceType: 'ranking', sourceId: String(item.id) });
  }

  async function openRankingDetails(item, trigger) {
    if (state.previewController) state.previewController.abort();
    const controller = new AbortController();
    state.previewController = controller;
    state.rankingDetail = item;
    text('preview-title', item.name || '排行榜详情');
    text('preview-summary', `${platforms[state.rankings.platform]} · ${item.update_frequency || '更新频率未知'}`);
    text('preview-source', '平台直连');
    $('preview-source').className = 'status-badge status-success';
    text('preview-limit-note', '按平台排名顺序展示榜单曲目。');
    show('preview-subscribe', true);
    show('preview-loading', true);
    show('preview-content', false);
    show('preview-error', false);
    $('preview-body').replaceChildren();
    openDialog('preview-dialog', trigger);
    try {
      const path = `/api/rankings/${encodeURIComponent(state.rankings.platform)}/${encodeURIComponent(item.id)}`;
      const detail = await request(path, { signal: controller.signal, timeout: 120000 });
      if (controller.signal.aborted || state.rankingDetail !== item) return;
      if (!detail || !Array.isArray(detail.tracks)) throw new Error('排行榜服务返回了无法识别的曲目列表。');
      const reported = Number.isFinite(detail.reported_count) ? detail.reported_count : detail.tracks.length;
      text('preview-title', detail.name || item.name || '排行榜详情');
      text('preview-summary', `${platforms[state.rankings.platform]} · ${item.update_frequency || '更新频率未知'} · 共 ${number(reported)} 首`);
      const rows = detail.tracks.map((track) => {
        const row = element('tr');
        const song = cell('歌曲');
        song.append(element('div', 'cell-title', track.name || '未命名歌曲'), element('code', 'cell-subtitle mono', track.id));
        const artist = cell('歌手'); artist.textContent = artistNames(track);
        const album = cell('专辑'); album.textContent = track.album || '—';
        row.append(song, artist, album);
        return row;
      });
      $('preview-body').replaceChildren(...rows);
      show('preview-table', rows.length > 0);
      show('preview-empty', rows.length === 0);
      show('preview-content', true);
    } catch (error) {
      if (error.name !== 'AbortError') { text('preview-error', error.message); show('preview-error', true); }
    } finally {
      if (state.previewController === controller) { state.previewController = null; show('preview-loading', false); }
    }
  }

  async function copyID(id) {
    try {
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(String(id));
      toast('歌曲平台标识已复制');
    } catch { toast('无法自动复制，请手动选择歌曲标识并复制。', true); }
  }

  function setupNavigation() {
    const shell = document.querySelector('.app-shell');
    const sidebar = $('sidebar');
    const slot = sidebar.parentElement;
    const drawer = $('navigation-drawer');
    const toggle = $('sidebar-toggle');
    const toggleIcon = $('sidebar-toggle-icon');
    const mobileButtons = [...document.querySelectorAll('.mobile-menu-button')];
    const mobile = window.matchMedia('(max-width: 760px)');
    const compact = window.matchMedia('(max-width: 1120px)');
    const preferenceKey = 'meloport.sidebar.collapsed';
    let preferredCollapsed;
    try {
      const saved = localStorage.getItem(preferenceKey);
      if (saved === 'true' || saved === 'false') preferredCollapsed = saved === 'true';
    } catch { /* A blocked storage policy should not disable the navigation. */ }

    const desktopCollapsed = () => preferredCollapsed ?? compact.matches;

    function updateToggle() {
      const expanded = mobile.matches ? drawer.open : !desktopCollapsed();
      const label = mobile.matches ? (expanded ? '关闭导航' : '打开导航') : (expanded ? '折叠侧栏' : '展开侧栏');
      toggle.setAttribute('aria-expanded', String(expanded));
      toggle.setAttribute('aria-controls', mobile.matches ? drawer.id : sidebar.id);
      if (mobile.matches) toggle.setAttribute('aria-haspopup', 'dialog');
      else toggle.removeAttribute('aria-haspopup');
      toggle.setAttribute('aria-label', label);
      toggle.title = label;
      toggleIcon.setAttribute('href', mobile.matches ? '#icon-menu' : '#icon-panel-left');
      const mobileLabel = drawer.open ? '关闭导航' : '打开导航';
      mobileButtons.forEach((button) => {
        button.setAttribute('aria-expanded', String(drawer.open));
        button.setAttribute('aria-controls', drawer.id);
        button.setAttribute('aria-haspopup', 'dialog');
        button.setAttribute('aria-label', mobileLabel);
        button.title = mobileLabel;
        const use = button.querySelector('use');
        if (use) use.setAttribute('href', '#icon-menu');
      });
    }

    function updateLayout() {
      const destination = mobile.matches ? drawer : slot;
      const moves = sidebar.parentElement !== destination;
      const hadSidebarFocus = moves && sidebar.contains(document.activeElement);
      if (!mobile.matches && drawer.open) {
        state.dialogTriggers.delete(drawer.id);
        drawer.close();
      }
      if (moves) destination.append(sidebar);
      shell.classList.toggle('sidebar-collapsed', !mobile.matches && desktopCollapsed());
      updateToggle();
      if (hadSidebarFocus) toggle.focus({ preventScroll: true });
    }

    function openDrawer(trigger) {
      state.dialogTriggers.set(drawer.id, trigger || document.activeElement);
      if (!drawer.open) drawer.showModal();
      updateToggle();
    }

    function closeDrawer() {
      if (drawer.open) drawer.close();
      else updateToggle();
    }

    toggle.addEventListener('click', () => {
      if (mobile.matches) {
        if (drawer.open) closeDrawer();
        else openDrawer(toggle);
        return;
      }
      preferredCollapsed = !desktopCollapsed();
      try { localStorage.setItem(preferenceKey, String(preferredCollapsed)); } catch { /* Keep the preference for this page. */ }
      updateLayout();
    });
    mobileButtons.forEach((button) => button.addEventListener('click', () => {
      if (!mobile.matches) return;
      if (drawer.open) closeDrawer();
      else openDrawer(button);
    }));
    $('drawer-close').addEventListener('click', closeDrawer);
    drawer.addEventListener('close', () => {
      updateToggle();
      const trigger = state.dialogTriggers.get(drawer.id);
      state.dialogTriggers.delete(drawer.id);
      if (trigger && trigger.isConnected) trigger.focus({ preventScroll: true });
    });
    drawer.addEventListener('click', (event) => {
      if (event.target === drawer) {
        const bounds = drawer.getBoundingClientRect();
        if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) drawer.close();
      } else if (event.target.closest('a[data-nav], a.brand')) closeDrawer();
    });
    mobile.addEventListener('change', updateLayout);
    compact.addEventListener('change', updateLayout);
    updateLayout();
  }

  const cloudPath = '/api/cloud115';
  const cloudPageSize = 15;

  function renderCloud() {
    const { data, dirty, operation } = state.cloud;
    if (!data) return;
    const active = data.runtime_enabled;
    const connected = data.session_authenticated || data.token_configured;
    const controlsReady = active && !operation && !data.restart_required;
    const connection = data.session_authenticated ? ['已扫码授权', 'success'] : data.token_configured ? ['已配置环境变量授权', 'neutral'] : ['未登录', active ? 'warning' : 'neutral'];
    text('cloud-auth-status', connection[0]);
    $('cloud-auth-status').className = `status-badge status-${connection[1]}`;
    text('cloud-auth-note', data.account?.name ? `账号：${data.account.name}` : connected
      ? '授权已配置，可浏览网盘并选择目标文件夹。'
      : '使用手机端 115 App 扫码，登录后可浏览并选择目标文件夹。');
    show('cloud-login', Boolean(data.login_available) && !data.session_authenticated);
    show('cloud-setup-link', !active && connected && !data.restart_required);
    show('cloud-logout', data.session_authenticated);
    show('cloud-check', active && connected);
    show('cloud-counts', active);
    show('cloud-restart', data.restart_required);
    $('cloud-login').disabled = !data.login_available || Boolean(operation);
    $('cloud-logout').disabled = Boolean(operation);
    $('cloud-check').disabled = !controlsReady || !connected;
    $('cloud-reconcile').disabled = !controlsReady || (!connected && !(data.stats?.confirmed > 0));
    $('cloud-fields').disabled = operation === 'save';
    $('cloud-save').disabled = Boolean(operation) || !dirty;
    renderCloudOptions();
    text('cloud-settings-note', dirty ? '有未保存的更改。' : '设置保存后，在下次启动时生效。');
    for (const key of ['pending', 'confirmed', 'exported']) text(`cloud-${key}`, number(data.stats?.[key]));
    renderCloudFiles();
    renderNavidrome();
  }

  function renderCloudOptions() {
    const cloudEnabled = $('cloud-enabled').checked;
    $('cloud-storage-fields').disabled = !cloudEnabled;
    $('cloud-target-cid').required = cloudEnabled;
    const connected = state.cloud.data?.session_authenticated || state.cloud.data?.token_configured;
    $('cloud-browse').disabled = !connected || Boolean(state.cloud.operation);
    $('cloud-browse').title = connected ? '' : '请先扫码登录 115 网盘';
    const selected = state.cloud.directorySelection;
    const matches = selected && selected.id === $('cloud-target-cid').value.trim();
    show('cloud-target-selection', Boolean(matches));
    if (matches) text('cloud-target-selection', `已选择：${selected.path}`);
  }

  function renderCloudDirectory() {
    const picker = state.cloud.picker;
    if (!picker) return;
    const current = picker.trail.at(-1);
    $('cloud-directory-path').replaceChildren(...picker.trail.flatMap((entry, index) => {
      const crumb = element('button', 'directory-crumb', entry.name);
      crumb.type = 'button';
      crumb.disabled = index === picker.trail.length - 1;
      if (crumb.disabled) crumb.setAttribute('aria-current', 'location');
      crumb.addEventListener('click', () => loadCloudDirectory(picker.trail.slice(0, index + 1)));
      return index ? [element('span', '', '/'), crumb] : [crumb];
    }));
    $('cloud-directory-list').replaceChildren(...picker.directories.map((entry) => {
      const button = element('button', 'directory-entry');
      button.type = 'button';
      button.append(icon('folder'), element('span', '', entry.name));
      button.setAttribute('aria-label', `打开文件夹 ${entry.name}`);
      button.addEventListener('click', () => loadCloudDirectory([...picker.trail, entry]));
      return button;
    }));
    show('cloud-directory-loading', picker.loading);
    cloudFeedback('cloud-directory-error', picker.error);
    show('cloud-directory-retry', Boolean(picker.error));
    show('cloud-directory-empty', picker.loaded && picker.directories.length === 0);
    show('cloud-directory-more', picker.loaded && picker.hasMore);
    $('cloud-directory-more').disabled = picker.loading;
    $('cloud-directory-select').disabled = !picker.loaded || picker.loading || current.id === '0';
    text('cloud-directory-selection', current.id === '0' ? '请选择一个子文件夹，根目录不可用。' : `目标：${current.name}`);
  }

  async function loadCloudDirectory(trail, more = false) {
    const picker = state.cloud.picker;
    if (!picker) return;
    if (picker.controller) picker.controller.abort();
    const controller = new AbortController();
    picker.controller = controller;
    picker.trail = trail;
    if (!more) { picker.directories = []; picker.offset = 0; }
    picker.loading = true;
    picker.loaded = false;
    picker.error = '';
    renderCloudDirectory();
    try {
      const cid = trail.at(-1).id;
      const data = await request(`${cloudPath}/directories?cid=${encodeURIComponent(cid)}&offset=${picker.offset}`, { signal: controller.signal, timeout: 95000 });
      if (state.cloud.picker !== picker || controller.signal.aborted) return;
      if (data.cid !== cid || !Array.isArray(data.directories) || data.directories.length > 100 ||
          data.directories.some((entry) => typeof entry.id !== 'string' || !/^[1-9][0-9]{0,19}$/.test(entry.id) || typeof entry.name !== 'string' || !entry.name) ||
          (data.has_more && data.directories.length === 0)) throw new Error('网盘返回了无法识别的文件夹列表。');
      picker.directories.push(...data.directories);
      picker.offset += data.directories.length;
      picker.hasMore = Boolean(data.has_more);
      picker.loaded = true;
    } catch (error) {
      if (state.cloud.picker === picker && !controller.signal.aborted) picker.error = error.message;
    } finally {
      if (state.cloud.picker === picker && picker.controller === controller) {
        picker.controller = null;
        picker.loading = false;
        renderCloudDirectory();
      }
    }
  }

  function cancelCloudDirectory() {
    if (state.cloud.picker?.controller) state.cloud.picker.controller.abort();
    state.cloud.picker = null;
    $('cloud-directory-list').replaceChildren();
    $('cloud-directory-path').replaceChildren();
  }

  function openCloudDirectory(trigger) {
    if (trigger.disabled) return;
    cancelCloudDirectory();
    state.cloud.picker = { trail: [], directories: [], offset: 0, loaded: false, loading: false, hasMore: false, error: '', controller: null };
    state.dialogTriggers.set('cloud-directory-dialog', trigger);
    $('cloud-directory-dialog').showModal();
    loadCloudDirectory([{ id: '0', name: '115 网盘' }]);
  }

  function selectCloudDirectory() {
    const picker = state.cloud.picker;
    const current = picker?.trail.at(-1);
    if (!current || current.id === '0' || !picker.loaded || picker.loading) return;
    $('cloud-target-cid').value = current.id;
    state.cloud.directorySelection = { id: current.id, path: picker.trail.map((entry) => entry.name).join(' / ') };
    state.cloud.dirty = true;
    closeDialog('cloud-directory-dialog');
    renderCloud();
    toast('目标文件夹已选择，请保存设置');
  }

  function renderNavidrome() {
    const { data, operation } = state.cloud;
    if (!data) return;
    const navidromeEnabled = $('navidrome-enabled').checked;
    const mediaEnabled = $('navidrome-media-enabled').checked;
    $('navidrome-fields').disabled = operation === 'save';
    $('navidrome-save').disabled = Boolean(operation) || !state.navidrome.dirty;
    $('navidrome-integration-fields').disabled = !navidromeEnabled;
    $('navidrome-media-fields').disabled = !mediaEnabled && !data.settings.enabled;
    $('navidrome-url').required = navidromeEnabled;
    $('navidrome-mount-root').required = navidromeEnabled;
    const status = data.navidrome_runtime_enabled
      ? data.settings.navidrome_enabled ? ['已启用', 'success'] : ['重启后关闭', 'warning']
      : data.settings.navidrome_enabled ? ['重启后启用', 'warning'] : ['未启用', 'neutral'];
    text('navidrome-runtime-status', status[0]);
    $('navidrome-runtime-status').className = `status-badge status-${status[1]}`;
    show('navidrome-restart', data.restart_required);
    text('navidrome-settings-note', state.navidrome.dirty ? '有未保存的更改。' : '设置保存后，在下次启动时生效。');
    text('navidrome-media-runtime', data.media_runtime_enabled
      ? '媒体端口已启动。更改媒体入口或 Navidrome 接入后，需重启服务。'
      : '媒体端口未启动。STRM 播放和 Navidrome 接入需开启后重启服务。');
  }

  function renderCloudFiles() {
    const { data } = state.cloud;
    const connected = data.session_authenticated || data.token_configured;
    const files = Array.isArray(data.files) ? data.files : [];
    const lastPage = Math.max(0, Math.ceil(files.length / cloudPageSize) - 1);
    state.cloud.page = Math.min(state.cloud.page, lastPage);
    const start = cloudPageSize * state.cloud.page;
    const rows = files.slice(start, start + cloudPageSize);
    const labels = { pending: ['待确认', 'warning'], confirmed: ['待导出', 'neutral'], exported: ['已生成', 'success'] };
    $('cloud-files').replaceChildren(...rows.map((file) => {
      const row = element('tr');
      const label = element('td');
      const name = String(file.relative_path || file.track_id).split('/').pop();
      label.append(element('strong', 'cell-title', name), element('p', 'field-hint cloud-file-task', file.task_id));
      if (file.strm_path) label.append(element('p', 'field-hint cloud-file-path', file.strm_path));
      const status = element('td');
      const [statusLabel, tone] = labels[file.status] || labels.pending;
      status.append(element('span', `status-badge status-${tone}`, statusLabel));
      row.append(label, element('td', '', file.task_id), status);
      return row;
    }));
    show('cloud-table', rows.length > 0);
    show('cloud-empty', rows.length === 0);
    text('cloud-empty-title', data.runtime_enabled ? '暂无导出记录' : 'STRM 导出未启用');
    text('cloud-empty-description', data.runtime_enabled
      ? connected ? '运行订阅任务，待 CD2 完成上传后即可生成。'
        : data.stats?.confirmed > 0 ? '已有确认的音频，可先修复 STRM；核对新的 CD2 上传需先登录。'
          : '先扫码登录，再核对 CD2 已上传的音频。'
      : '当前仅下载到本地。需要 CD2 上传和 STRM 时再开启 115 交接。');
    $('cloud-empty').classList.toggle('cloud-empty-setup', !data.runtime_enabled);
    show('cloud-pagination', lastPage > 0 || Boolean(data.files_truncated));
    text('cloud-file-count', `${start + 1}–${start + rows.length} / ${files.length} 条记录${data.files_truncated ? '（仅显示前 100 条）' : ''}`);
    $('cloud-prev').disabled = state.cloud.page === 0;
    $('cloud-next').disabled = state.cloud.page >= lastPage;
  }

  function cloudFeedback(id, message) {
    text(id, message);
    show(id, Boolean(message));
  }

  async function loadCloud() {
    if (state.cloud.controller) state.cloud.controller.abort();
    const controller = new AbortController();
    state.cloud.controller = controller;
    show('cloud-loading', !state.cloud.data);
    show('navidrome-loading', !state.cloud.data);
    cloudFeedback('cloud-error', '');
    cloudFeedback('navidrome-error', '');
    try {
      const data = await request(cloudPath, { timeout: 20000, signal: controller.signal });
      if (controller.signal.aborted) return;
      state.cloud.data = data;
      for (const [fields, dirty] of [['cloud-fields', state.cloud.dirty], ['navidrome-fields', state.navidrome.dirty]]) {
        if (dirty) continue;
        for (const input of $(fields).querySelectorAll('input[name]')) {
          if (input.type === 'checkbox') input.checked = Boolean(data.settings[input.name]);
          else input.value = data.settings[input.name] ?? '';
        }
      }
      renderCloud();
    } catch (error) {
      if (!controller.signal.aborted) {
        cloudFeedback('cloud-error', error.message);
        cloudFeedback('navidrome-error', error.message);
      }
    } finally {
      if (state.cloud.controller === controller) {
        state.cloud.controller = null;
        show('cloud-loading', false);
        show('navidrome-loading', false);
      }
    }
  }

  async function cloudAction(button, action, busyText, fn, errorID = 'cloud-error') {
    if (state.cloud.operation || button.disabled) return;
    state.cloud.operation = action;
    cloudFeedback(errorID, '');
    try {
      await busyButton(button, busyText, async () => {
        renderCloud();
        await fn();
      });
    } catch (error) {
      cloudFeedback(errorID, error.message);
      if (errorID.endsWith('-settings-error')) $(errorID).focus();
    } finally {
      state.cloud.operation = '';
      renderCloud();
    }
  }

  function readCloudSettings(form, saved = {}) {
    const fields = Object.fromEntries(Array.from(form.querySelectorAll('input[name]'), (input) => [
      input.name, input.type === 'checkbox' ? input.checked : input.value.trim()
    ]));
    return { ...saved, ...fields };
  }

  function saveCloudSettings(event) {
    event.preventDefault();
    if (!state.cloud.data || state.cloud.operation) return;
    const settings = readCloudSettings(event.currentTarget, state.cloud.data.settings);
    if (settings.enabled && !/^[1-9][0-9]{0,19}$/.test(settings.target_cid)) {
      cloudFeedback('cloud-settings-error', '启用 115 时，请填写非根目录的数字 CID。');
      $('cloud-target-cid').focus();
      return;
    }
    cloudAction($('cloud-save'), 'save', '正在保存', async () => {
      const result = await request(`${cloudPath}/settings`, { method: 'PUT', body: settings });
      state.cloud.data = { ...state.cloud.data, settings, restart_required: result.restart_required };
      state.cloud.dirty = false;
      await loadCloud();
      toast(result.restart_required ? '设置已保存，重启服务后生效' : '网盘交接设置已保存');
    }, 'cloud-settings-error');
  }

  function saveNavidromeSettings(event) {
    event.preventDefault();
    if (!state.cloud.data || state.cloud.operation) return;
    const settings = readCloudSettings(event.currentTarget, state.cloud.data.settings);
    if (settings.navidrome_enabled && !settings.media_enabled) {
      cloudFeedback('navidrome-settings-error', '接入 Navidrome 时，请同时开启独立媒体端口。');
      $('navidrome-media-enabled').focus();
      return;
    }
    if (settings.navidrome_enabled && (!settings.navidrome_url || !settings.navidrome_mount_root)) {
      const missing = $(settings.navidrome_url ? 'navidrome-mount-root' : 'navidrome-url');
      cloudFeedback('navidrome-settings-error', '请同时填写 Navidrome 服务地址与音乐目录。');
      missing.focus();
      return;
    }
    cloudAction($('navidrome-save'), 'save', '正在保存', async () => {
      const result = await request(`${cloudPath}/settings`, { method: 'PUT', body: settings });
      state.cloud.data = { ...state.cloud.data, settings, restart_required: result.restart_required };
      state.navidrome.dirty = false;
      await loadCloud();
      toast(result.restart_required ? 'Navidrome 设置已保存，重启服务后生效' : 'Navidrome 设置已保存');
    }, 'navidrome-settings-error');
  }

  function cloudSessionPath(id) {
    return `${cloudPath}/login?session_id=${encodeURIComponent(id)}`;
  }

  function cancelCloudLogin() {
    const session = state.cloud.login;
    if (!session) return;
    state.cloud.login = null;
    clearTimeout(session.timer);
    session.controller.abort();
    if (session.qrURL) URL.revokeObjectURL(session.qrURL);
    $('cloud-qr').replaceChildren();
    if (session.id) request(cloudSessionPath(session.id), { method: 'DELETE', timeout: 5000 }).catch(() => {});
  }

  function renderCloudLogin(message, { qr = false, loading = false, retry = false, error = false } = {}) {
    text('cloud-login-status', message);
    $('cloud-login-status').classList.toggle('is-error', error);
    show('cloud-qr', qr);
    show('cloud-qr-loading', loading);
    show('cloud-qr-frame', qr || loading);
    show('cloud-login-retry', retry);
  }

  async function cloudQRImage(path, id, signal) {
    if (path !== `${cloudPath}/login/qr?session_id=${encodeURIComponent(id)}`) throw new Error('服务返回了无效的二维码地址。');
    const response = await fetch(path, { headers: { 'X-Meloport-Request': '1' }, mode: 'same-origin', redirect: 'error', cache: 'no-store', signal });
    if (!response.ok || response.headers.get('Content-Type') !== 'image/png') throw new Error('无法读取二维码，请重新获取。');
    const blob = await response.blob();
    if (blob.type !== 'image/png' || blob.size === 0) throw new Error('无法读取二维码，请重新获取。');
    return URL.createObjectURL(blob);
  }

  async function pollCloudLogin(session) {
    if (state.cloud.login !== session) return;
    try {
      const result = await request(cloudSessionPath(session.id), { signal: session.controller.signal, timeout: 95000 });
      if (state.cloud.login !== session) return;
      if (result.status === 'authorized') {
        state.cloud.login = null;
        clearTimeout(session.timer);
        session.controller.abort();
        if (session.qrURL) URL.revokeObjectURL(session.qrURL);
        closeDialog('cloud-login-dialog');
        await loadCloud();
        toast('115 网盘已授权，可以选择目标文件夹');
        return;
      }
      if (result.status === 'waiting' || result.status === 'scanned') {
        renderCloudLogin(result.status === 'scanned' ? '已扫码，请在手机上确认。' : '等待扫码…', { qr: true });
        session.timer = setTimeout(() => pollCloudLogin(session), 1800);
        return;
      }
      const labels = { expired: '二维码已过期，请重新获取。', cancelled: '手机端已取消授权。' };
      state.cloud.login = null;
      clearTimeout(session.timer);
      session.controller.abort();
      if (session.qrURL) URL.revokeObjectURL(session.qrURL);
      renderCloudLogin(labels[result.status] || result.error || '授权未完成，请重新获取二维码。', { retry: true, error: true });
    } catch (error) {
      if (state.cloud.login === session) renderCloudLogin(error.message, { retry: true, error: true });
    }
  }

  async function startCloudLogin(trigger) {
    cancelCloudLogin();
    const dialog = $('cloud-login-dialog');
    if (!dialog.open) {
      state.dialogTriggers.set(dialog.id, trigger);
      dialog.showModal();
    }
    const session = { id: '', timer: null, qrURL: '', controller: new AbortController() };
    state.cloud.login = session;
    renderCloudLogin('正在获取二维码…', { loading: true });
    try {
      const result = await request(`${cloudPath}/login`, { method: 'POST', timeout: 25000 });
      session.id = result.session_id;
      if (state.cloud.login !== session) {
        if (session.id) request(cloudSessionPath(session.id), { method: 'DELETE', timeout: 5000 }).catch(() => {});
        return;
      }
      const qrURL = await cloudQRImage(result.qrcode, session.id, session.controller.signal);
      if (state.cloud.login !== session) {
        URL.revokeObjectURL(qrURL);
        return;
      }
      session.qrURL = qrURL;
      const qr = element('img');
      qr.alt = '115 登录二维码';
      qr.width = qr.height = 224;
      qr.addEventListener('error', () => {
        if (state.cloud.login === session) {
          cancelCloudLogin();
          renderCloudLogin('二维码无法显示，请重新获取。', { retry: true, error: true });
        }
      }, { once: true });
      qr.addEventListener('load', () => {
        if (state.cloud.login !== session) return;
        renderCloudLogin('等待扫码…', { qr: true });
        session.timer = setTimeout(() => pollCloudLogin(session), 1800);
      }, { once: true });
      $('cloud-qr').replaceChildren(qr);
      renderCloudLogin('正在显示二维码…', { qr: true });
      qr.src = session.qrURL;
    } catch (error) {
      if (state.cloud.login === session) renderCloudLogin(error.message, { retry: true, error: true });
    }
  }

  function route() {
    if (!state.authenticated) return;
    const requested = window.location.hash.slice(1) || 'overview';
    const next = Object.hasOwn(titles, requested) ? requested : 'overview';
    if (requested !== next) history.replaceState(null, '', `#${next}`);
    document.querySelectorAll('[data-view]').forEach((view) => { view.hidden = view.dataset.view !== next; });
    document.querySelectorAll('[data-nav]').forEach((link) => {
      if (link.dataset.nav === next) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    document.title = `${titles[next]} · Meloport`;
    const previous = state.route;
    const changed = previous && next !== previous;
    state.route = next;
    if (changed) { $(`${next}-title`).focus({ preventScroll: true }); window.scrollTo({ top: 0, behavior: 'instant' }); }
    if (next === 'rankings') loadRankings(state.rankings.platform);
    if (previous === 'audio' && next !== 'audio' && state.audio.controller) state.audio.controller.abort();
    if (next === 'audio' && previous !== 'audio') loadAudio();
    if (['cloud115', 'navidrome'].includes(next) && previous !== next) loadCloud();
  }

  $('refresh-button').addEventListener('click', async (event) => {
    const button = event.currentTarget;
    if (button.disabled) return;
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    try { await refreshOverview({ force: true }); }
    finally {
      button.disabled = false;
      button.removeAttribute('aria-busy');
    }
  });
  $('add-task-button').addEventListener('click', (event) => openTask(null, event.currentTarget));
  $('empty-add-task-button').addEventListener('click', (event) => openTask(null, event.currentTarget));
  $('task-enabled').addEventListener('change', updateTaskSubmit);
  $('task-filter').addEventListener('input', () => renderTasks());
  $('task-platform').addEventListener('change', () => { renderTaskQuality($('task-platform').value); renderTaskSource(); });
  $('task-source-type').addEventListener('change', renderTaskSource);
  $('task-form').addEventListener('submit', saveTask);
  $('delete-confirm').addEventListener('click', deleteTask);
  $('history-task-filter').addEventListener('change', () => renderHistory(true));
  $('history-status-filter').addEventListener('change', () => renderHistory(true));
  $('settings-form').addEventListener('input', markSettingsDirty);
  $('settings-form').addEventListener('change', markSettingsDirty);
  $('settings-form').addEventListener('submit', saveSettings);
  $('cloud-settings').addEventListener('input', () => { state.cloud.dirty = true; renderCloud(); });
  $('cloud-settings').addEventListener('submit', saveCloudSettings);
  $('navidrome-settings').addEventListener('input', () => { state.navidrome.dirty = true; renderNavidrome(); });
  $('navidrome-settings').addEventListener('submit', saveNavidromeSettings);
  $('cloud-refresh').addEventListener('click', (event) => busyButton(event.currentTarget, '正在刷新', loadCloud));
  $('navidrome-refresh').addEventListener('click', (event) => busyButton(event.currentTarget, '正在刷新', loadCloud));
  $('cloud-setup-link').addEventListener('click', (event) => {
    event.preventDefault();
    $('cloud-settings-title').focus();
    $('cloud-settings-title').scrollIntoView({ block: 'start' });
  });
  $('cloud-login').addEventListener('click', (event) => startCloudLogin(event.currentTarget));
  $('cloud-login-retry').addEventListener('click', (event) => startCloudLogin(event.currentTarget));
  $('cloud-login-dialog').addEventListener('close', cancelCloudLogin);
  $('cloud-browse').addEventListener('click', (event) => openCloudDirectory(event.currentTarget));
  $('cloud-directory-dialog').addEventListener('close', cancelCloudDirectory);
  $('cloud-directory-select').addEventListener('click', selectCloudDirectory);
  $('cloud-directory-retry').addEventListener('click', () => loadCloudDirectory(state.cloud.picker.trail));
  $('cloud-directory-more').addEventListener('click', () => loadCloudDirectory(state.cloud.picker.trail, true));
  $('cloud-logout').addEventListener('click', (event) => cloudAction(event.currentTarget, 'logout', '正在退出', async () => {
    await request(`${cloudPath}/session`, { method: 'DELETE' });
    cloudFeedback('cloud-check-result', '');
    await loadCloud();
    toast('已退出扫码授权');
  }));
  $('cloud-check').addEventListener('click', (event) => cloudAction(event.currentTarget, 'check', '正在检查', async () => {
    cloudFeedback('cloud-check-result', '');
    const result = await request(`${cloudPath}/check`, { method: 'POST', timeout: 95000 });
    await loadCloud();
    const name = result.account?.name;
    cloudFeedback('cloud-check-result', name ? `${name} 的授权有效，目标目录可以访问。` : '115 授权有效，目标目录可以访问。');
  }));
  $('cloud-reconcile').addEventListener('click', (event) => cloudAction(event.currentTarget, 'reconcile', '正在核对', async () => {
    cloudFeedback('cloud-reconcile-result', '');
    try {
      await request(`${cloudPath}/reconcile`, { method: 'POST', timeout: 115000 });
      cloudFeedback('cloud-reconcile-result', '核对完成，导出状态已更新。');
    } finally {
      await loadCloud();
    }
  }));
  $('cloud-prev').addEventListener('click', () => { state.cloud.page--; renderCloudFiles(); });
  $('cloud-next').addEventListener('click', () => { state.cloud.page++; renderCloudFiles(); });
  $('search-form').addEventListener('submit', (event) => { event.preventDefault(); searchSongs(0, true); });
  $('search-prev').addEventListener('click', () => searchSongs(Math.max(0, state.search.offset - state.search.limit)));
  $('search-next').addEventListener('click', () => searchSongs(state.search.offset + state.search.limit));
  $('audio-filters').addEventListener('submit', (event) => {
    event.preventDefault();
    state.audio.filters = Object.fromEntries([...new FormData(event.currentTarget)].map(([key, value]) => [key, value.trim()]));
    showAudioRepair('');
    loadAudio([0]);
  });
  $('audio-refresh').addEventListener('click', () => loadAudio());
  $('audio-prev').addEventListener('click', () => {
    if (state.audio.offsets.length > 1) loadAudio(state.audio.offsets.slice(0, -1));
  });
  $('audio-next').addEventListener('click', () => {
    if (state.audio.nextOffset !== null) loadAudio([...state.audio.offsets, state.audio.nextOffset]);
  });
  $('rankings-filter').addEventListener('input', (event) => { state.rankings.filter = event.target.value; renderRankings(); });
  $('rankings-category').addEventListener('change', (event) => { state.rankings.category = event.target.value; renderRankings(); });
  document.querySelectorAll('#rankings-platform [data-platform]').forEach((button) => button.addEventListener('click', () => loadRankings(button.dataset.platform)));
  $('rankings-refresh').addEventListener('click', async (event) => {
    await busyButton(event.currentTarget, '', () => loadRankings(state.rankings.platform, true));
  });
  $('rankings-retry').addEventListener('click', () => loadRankings(state.rankings.platform, true));
  $('preview-subscribe').addEventListener('click', (event) => {
    const item = state.rankingDetail;
    if (!item) return;
    const trigger = event.currentTarget;
    const platform = state.rankings.platform;
    state.rankingDetail = null;
    state.dialogTriggers.delete('preview-dialog');
    if ($('preview-dialog').open) $('preview-dialog').close();
    if (state.previewController) { state.previewController.abort(); state.previewController = null; }
    openTask(null, trigger, { platform, sourceType: 'ranking', sourceId: String(item.id) });
  });
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
  document.querySelectorAll('dialog:not(#navigation-drawer)').forEach((dialog) => {
    dialog.addEventListener('cancel', (event) => { if (dialog.getAttribute('aria-busy') === 'true') event.preventDefault(); });
    dialog.addEventListener('close', () => {
      if (dialog.id === 'preview-dialog') {
        if (state.previewController) { state.previewController.abort(); state.previewController = null; }
        state.rankingDetail = null;
        show('preview-subscribe', false);
      }
      const trigger = state.dialogTriggers.get(dialog.id);
      if (trigger && trigger.isConnected) trigger.focus({ preventScroll: true });
    });
  });
  document.addEventListener('visibilitychange', () => {
    clearTimeout(state.pollTimer);
    if (state.authenticated && !document.hidden) refreshOverview({ quiet: true, force: true });
  });
  window.addEventListener('hashchange', route);
  window.addEventListener('pagehide', () => {
    clearTimeout(state.pollTimer);
    if (state.overviewController) state.overviewController.abort();
    if (state.previewController) state.previewController.abort();
    if (state.search.controller) state.search.controller.abort();
    if (state.rankings.controller) state.rankings.controller.abort();
    if (state.audio.controller) state.audio.controller.abort();
    if (state.cloud.controller) state.cloud.controller.abort();
    cancelCloudLogin();
    cancelCloudDirectory();
  });
  setupSchedule('settings');
  setupSchedule('task');
  setupNavigation();
  function lockSession(message = '') {
    state.authenticated = false;
    state.overview = null;
    state.route = '';
    ++state.overviewVersion;
    clearTimeout(state.pollTimer);
    for (const controller of [state.overviewController, state.previewController, state.search.controller, state.rankings.controller, state.audio.controller, state.cloud.controller]) if (controller) controller.abort();
    state.overviewPromise = null;
    cancelCloudLogin();
    document.querySelectorAll('dialog[open]').forEach((dialog) => dialog.close());
    show('app-shell', false);
    $('toast-region').replaceChildren();
    show('console-skip-link', false);
    show('login-view', true);
    document.title = '登录 · Meloport';
    text('login-status', '');
    text('login-error', message);
    show('login-error', Boolean(message));
    $('login-password').value = '';
  }

  function unlockSession() {
    $('login-fields').disabled = false;
    state.authenticated = true;
    show('login-view', false);
    show('app-shell', true);
    show('console-skip-link', true);
    route();
    refreshOverview({ force: true });
  }

  async function checkSession() {
    $('login-fields').disabled = true;
    text('login-status', '正在检查登录状态…');
    show('login-retry', false);
    try {
      const session = await request('/api/session', { timeout: 15000 });
      if (session.authenticated) { unlockSession(); return; }
      lockSession();
      show('login-setup', !session.configured);
      $('login-fields').disabled = !session.configured;
      text('login-status', session.configured ? '' : '管理员密钥尚未配置。');
      show('login-retry', !session.configured);
    } catch (error) {
      lockSession(error.message);
      text('login-status', '无法检查登录状态。');
      show('login-retry', true);
    }
  }

  $('login-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!$('login-password').value) { text('login-error', '请输入管理员密钥。'); show('login-error', true); $('login-password').focus(); return; }
    await busyButton($('login-submit'), '正在登录', async () => {
      show('login-error', false);
      try {
        await request('/api/session', { method: 'POST', body: { password: $('login-password').value } });
        $('login-password').value = '';
        unlockSession();
      } catch (error) {
        text('login-error', error.message); show('login-error', true);
        $('login-password').value = ''; $('login-password').focus();
      }
    });
  });
  $('login-retry').addEventListener('click', checkSession);
  $('logout-button').addEventListener('click', async () => {
    await busyButton($('logout-button'), '', async () => {
      try { await request('/api/session', { method: 'DELETE' }); lockSession(); $('login-password').focus(); }
      catch (error) { toast(error.message, true); }
    });
  });
  checkSession();
}, { once: true });
