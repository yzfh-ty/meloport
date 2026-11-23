const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'assets/app.js'), 'utf8');

function directoryFixture(request) {
  const nodes = new Map();
  const node = id => {
    if (!nodes.has(id)) nodes.set(id, { value: '', replaceChildren() {} });
    return nodes.get(id);
  };
  const context = {
    state: { cloud: { picker: { trail: [], directories: [], offset: 0 }, dirty: false } },
    $: node, AbortController, request, cloudPath: '/api/cloud115',
    renderCloudDirectory() {}, renderCloud() {}, closeDialog() {}, toast() {},
  };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('  async function loadCloudDirectory('), source.indexOf('  function renderNavidrome(')) + '\nthis.load = loadCloudDirectory; this.cancel = cancelCloudDirectory; this.select = selectCloudDirectory;', context);
  return context;
}

test('folder browsing ignores stale requests and cancellation does not restore private listings', async () => {
  const pending = [];
  const context = directoryFixture((url, options) => new Promise(resolve => pending.push({ url, options, resolve })));
  const first = context.load([{ id: '0', name: '网盘' }]);
  const second = context.load([{ id: '10', name: '音乐' }]);
  assert.equal(pending[0].options.signal.aborted, true);
  pending[0].resolve({ cid: '0', directories: [{ id: '11', name: '旧结果' }] });
  await first;
  assert.equal(context.state.cloud.picker.loading, true);
  pending[1].resolve({ cid: '10', directories: [{ id: '12', name: '收藏' }], has_more: false });
  await second;
  assert.equal(context.state.cloud.picker.directories[0].name, '收藏');
  const third = context.load([{ id: '12', name: '收藏' }]);
  context.cancel();
  assert.equal(pending[2].options.signal.aborted, true);
  pending[2].resolve({ cid: '12', directories: [{ id: '13', name: '不能恢复' }] });
  await third;
  assert.equal(context.state.cloud.picker, null);
});

test('folder selection rejects root and failed reads, preserves drafts, and supports retry', async () => {
  let fail = false;
  const context = directoryFixture(async url => {
    if (fail) throw new Error('请重新扫码');
    return { cid: new URL(url, 'http://fixture').searchParams.get('cid'), directories: [], has_more: false };
  });
  context.$('cloud-target-cid').value = 'saved-target';
  await context.load([{ id: '0', name: '网盘' }]);
  context.select();
  assert.equal(context.$('cloud-target-cid').value, 'saved-target');
  fail = true;
  await context.load([{ id: '10', name: '音乐' }]);
  assert.equal(context.state.cloud.picker.error, '请重新扫码');
  context.select();
  assert.equal(context.$('cloud-target-cid').value, 'saved-target');
  fail = false;
  await context.load([{ id: '0', name: '网盘' }, { id: '10', name: '音乐' }]);
  context.select();
  assert.equal(context.$('cloud-target-cid').value, '10');
  assert.equal(context.state.cloud.dirty, true);
  assert.equal(context.state.cloud.directorySelection.path, '网盘 / 音乐');
});

test('settings hints omit resolved server directories while preserving editable values and drafts', () => {
  const privateDirectory = '/private/server-only/library';
  const nodes = new Map();
  const node = id => {
    if (!nodes.has(id)) nodes.set(id, { value: '', textContent: '' });
    return nodes.get(id);
  };
  const state = {
    overview: { music_dir: 'library', music_dir_resolved: privateDirectory, timezone: 'UTC', schedule: '0 3 * * *', quality: 'hires', api_key_configured: false },
    settingsDirty: false, busy: new Set(),
  };
  const context = { state, $: node, text: (id, value) => { node(id).textContent = value; }, setSchedule() {} };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('  function markSettingsDirty('), source.indexOf('  async function saveSettings(')) + '\nthis.render = renderSettings; this.mark = markSettingsDirty;', context);
  context.render();
  assert.equal(node('settings-music-dir').value, 'library');
  assert.equal(node('settings-timezone').value, 'UTC');
  assert.equal(node('settings-quality').value, 'hires');
  node('settings-music-dir').value = 'library-draft';
  context.mark();
  context.render();
  assert.equal(node('settings-music-dir').value, 'library-draft');
  assert.match(node('settings-music-dir-hint').textContent, /保存后/);
  context.render(true);
  assert.equal(node('settings-music-dir').value, 'library');
  assert.match(node('settings-music-dir-hint').textContent, /已有音频会保留/);
  for (const value of nodes.values()) assert.ok(!value.textContent.includes(privateDirectory));
});

test('subscription creation messages distinguish downloads queued from subscriptions only saved', () => {
  const nodes = { 'task-enabled': { checked: true }, 'task-submit': {}, 'task-enabled-hint': {} };
  const context = { state: { editTask: null }, $: id => nodes[id], text: (id, value) => { nodes[id].textContent = value; } };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('  function updateTaskSubmit('), source.indexOf('  async function saveTask(')) + '\nthis.update = updateTaskSubmit; this.message = taskSavedMessage;', context);
  context.update();
  assert.equal(nodes['task-submit'].textContent, '添加并开始下载');
  assert.match(nodes['task-enabled-hint'].textContent, /立即下载/);
  assert.match(context.message({ id: 'manual', enabled: true }, { queued: true }, null), /首次下载已排队/);
  assert.match(context.message({ id: 'manual', enabled: true }, { queued: false, queue_reason: 'paused' }, null), /队列暂停/);
  assert.match(context.message({ id: 'manual', enabled: true }, { queued: false, queue_reason: 'full' }, null), /队列已满/);
  nodes['task-enabled'].checked = false;
  context.update();
  assert.equal(nodes['task-submit'].textContent, '添加订阅');
  assert.match(nodes['task-enabled-hint'].textContent, /只保存订阅/);
  assert.match(context.message({ id: 'manual', enabled: false }, { queued: false }, null), /自动同步未启用/);
  nodes['task-enabled'].checked = true;
  context.state.editTask = { id: 'manual' };
  context.update();
  assert.equal(nodes['task-submit'].textContent, '保存修改');
  assert.match(nodes['task-enabled-hint'].textContent, /不会立即下载/);
  assert.equal(context.message({ id: 'manual' }, { saved: true }, context.state.editTask), '订阅 manual 已更新');
});

test('disabled integrations preserve their addresses while saving explicit off switches', () => {
  const inputs = [
    { name: 'enabled', type: 'checkbox', checked: false },
    { name: 'media_enabled', type: 'checkbox', checked: false },
    { name: 'navidrome_enabled', type: 'checkbox', checked: false },
    { name: 'navidrome_playlist_sync', type: 'checkbox', checked: false },
    { name: 'media_base_url', type: 'url', value: ' http://127.0.0.1:18081 ', disabled: true },
    { name: 'navidrome_url', type: 'url', value: ' http://127.0.0.1:4533 ', disabled: true },
    { name: 'navidrome_mount_root', type: 'text', value: '/music', disabled: true },
  ];
  const form = { querySelectorAll: () => inputs };
  const context = {};
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('  function readCloudSettings('), source.indexOf('  function saveCloudSettings(')) + '\nthis.read = readCloudSettings;', context);
  const settings = context.read(form);
  assert.equal(settings.enabled, false);
  assert.equal(settings.media_enabled, false);
  assert.equal(settings.navidrome_enabled, false);
  assert.equal(settings.navidrome_playlist_sync, false);
  assert.equal(settings.media_base_url, 'http://127.0.0.1:18081');
  assert.equal(settings.navidrome_url, 'http://127.0.0.1:4533');
  assert.equal(settings.navidrome_mount_root, '/music');
});

test('integration page saves preserve the other page and do not submit its draft', () => {
  const saved = {
    enabled: true, target_cid: '12345', mount_dir: 'mounted-115', strm_dir: 'strm',
    media_enabled: true, media_base_url: 'https://media.example.com',
    navidrome_enabled: true, navidrome_url: 'http://127.0.0.1:4533',
    navidrome_mount_root: '/music', navidrome_playlist_sync: false,
  };
  const cloudInputs = [
    { name: 'enabled', type: 'checkbox', checked: true },
    { name: 'target_cid', type: 'text', value: ' 67890 ' },
    { name: 'mount_dir', type: 'text', value: 'mount-reviewed' },
    { name: 'strm_dir', type: 'text', value: 'strm-reviewed' },
  ];
  const navidromeInputs = [
    { name: 'navidrome_enabled', type: 'checkbox', checked: false },
    { name: 'navidrome_url', type: 'url', value: ' http://127.0.0.1:4534 ', disabled: true },
    { name: 'navidrome_mount_root', type: 'text', value: '/music-reviewed', disabled: true },
    { name: 'navidrome_playlist_sync', type: 'checkbox', checked: false },
    { name: 'media_enabled', type: 'checkbox', checked: true },
    { name: 'media_base_url', type: 'url', value: ' https://media.example.com/new ' },
  ];
  const context = {};
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('  function readCloudSettings('), source.indexOf('  function saveCloudSettings(')) + '\nthis.read = readCloudSettings;', context);
  const cloudSaved = context.read({ querySelectorAll: () => cloudInputs }, saved);
  assert.equal(cloudSaved.target_cid, '67890');
  assert.equal(cloudSaved.navidrome_enabled, true);
  assert.equal(cloudSaved.navidrome_url, saved.navidrome_url);
  assert.equal(cloudSaved.media_base_url, saved.media_base_url);
  const navidromeSaved = context.read({ querySelectorAll: () => navidromeInputs }, cloudSaved);
  assert.equal(navidromeSaved.target_cid, '67890');
  assert.equal(navidromeSaved.mount_dir, 'mount-reviewed');
  assert.equal(navidromeSaved.strm_dir, 'strm-reviewed');
  assert.equal(navidromeSaved.navidrome_enabled, false);
  assert.equal(navidromeSaved.navidrome_url, 'http://127.0.0.1:4534');
  assert.equal(navidromeSaved.media_base_url, 'https://media.example.com/new');
  assert.equal(saved.target_cid, '12345');
  assert.equal(saved.navidrome_enabled, true);
});

test('refreshing integration settings preserves each unsaved page independently', async () => {
  const cloudInput = { name: 'target_cid', type: 'text', value: 'cloud-draft' };
  const navidromeInput = { name: 'navidrome_url', type: 'url', value: 'navidrome-draft' };
  const state = { cloud: { data: null, dirty: true, controller: null }, navidrome: { dirty: false } };
  const context = {
    state, AbortController, cloudPath: '/api/cloud115', show() {}, cloudFeedback() {}, renderCloud() {},
    $: id => ({ querySelectorAll: () => id === 'cloud-fields' ? [cloudInput] : [navidromeInput] }),
    request: async () => ({ settings: { target_cid: 'saved-cid', navidrome_url: 'saved-navidrome-url' } }),
  };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('  async function loadCloud('), source.indexOf('  async function cloudAction(')) + '\nthis.load = loadCloud;', context);
  await context.load();
  assert.equal(cloudInput.value, 'cloud-draft');
  assert.equal(navidromeInput.value, 'saved-navidrome-url');
  state.cloud.dirty = false;
  state.navidrome.dirty = true;
  navidromeInput.value = 'navidrome-draft';
  await context.load();
  assert.equal(cloudInput.value, 'saved-cid');
  assert.equal(navidromeInput.value, 'navidrome-draft');
});

test('search loading, failure and cancellation describe the current request', async () => {
  for (const name of ['Error', 'AbortError']) {
    const nodes = new Map();
    const node = (id) => {
      if (!nodes.has(id)) nodes.set(id, { value: '', removeAttribute() {} });
      return nodes.get(id);
    };
    node('search-keyword').value = '林俊杰 江南';
    node('search-platform').value = 'qq';
    node('search-results-summary').textContent = '网易云音乐 · “周杰伦 晴天” · 本页 20 首';
    let rejectRequest;
    const pending = new Promise((_, reject) => { rejectRequest = reject; });
    const context = {
      $: node, text: (id, value) => { node(id).textContent = value; },
      show: (id, visible) => { node(id).hidden = !visible; },
      platforms: { qq: 'QQ 音乐' }, AbortController, URLSearchParams,
      busyButton: async (_, __, action) => action(), request: () => pending,
      state: { search: { loading: false, limit: 20, offset: 0, tracks: [] } },
    };
    vm.createContext(context);
    vm.runInContext(source.slice(source.indexOf('  async function searchSongs('), source.indexOf('  function renderSearchResults(')) + '\nthis.search = searchSongs;', context);
    const done = context.search(0, true);
    assert.match(node('search-results-summary').textContent, /QQ 音乐.*林俊杰 江南.*正在检索/);
    assert.ok(!node('search-results-summary').textContent.includes('周杰伦'));
    assert.equal(node('search-results-table').hidden, true);
    assert.equal(node('search-keyword').disabled, true);
    const error = new Error('request failed'); error.name = name;
    rejectRequest(error);
    await done;
    assert.match(node('search-results-summary').textContent, /QQ 音乐.*林俊杰 江南/);
    assert.ok(node('search-results-summary').textContent.includes(name === 'AbortError' ? '已取消' : '未完成'));
    assert.equal(node('search-keyword').disabled, false);
    assert.equal(node('search-loading').hidden, true);
  }
});

test('history distinguishes filtered results from an empty database and restores the initial copy', () => {
  const nodes = new Map();
  const node = (id) => {
    if (!nodes.has(id)) nodes.set(id, { value: '' });
    return nodes.get(id);
  };
  const state = { overview: { tasks: [], runs: [] }, historyOptionsSignature: '[]', historySignature: '[[],"",""]' };
  const context = {
    state, $: node, number: String,
    text: (id, value) => { node(id).textContent = value; },
    show: (id, visible) => { node(id).hidden = !visible; },
  };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('  function renderHistory('), source.indexOf('  function markSettingsDirty(')) + '\nthis.render = renderHistory;', context);
  context.render();
  assert.equal(node('history-empty-title').textContent, '还没有运行记录');
  state.overview.runs = [{ task_id: 'one', status: 'failed' }];
  state.historyOptionsSignature = '["one"]';
  node('history-status-filter').value = 'success';
  state.historySignature = '[[],"","success"]';
  context.render();
  assert.equal(node('history-empty-title').textContent, '没有符合筛选条件的记录');
  assert.match(node('history-empty-description').textContent, /调整任务或状态/);
  assert.equal(node('history-empty').hidden, false);
  state.overview.runs = [];
  state.historyOptionsSignature = '[]';
  context.render();
  assert.equal(node('history-empty-title').textContent, '还没有运行记录');
  assert.match(node('history-empty-description').textContent, /执行一次同步/);
});
