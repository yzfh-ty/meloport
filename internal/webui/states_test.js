const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'assets/app.js'), 'utf8');

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
    enabled: true, target_cid: '12345', staging_dir: 'staging', strm_dir: 'strm',
    media_enabled: true, media_base_url: 'https://media.example.com',
    navidrome_enabled: true, navidrome_url: 'http://127.0.0.1:4533',
    navidrome_mount_root: '/music', navidrome_playlist_sync: false,
  };
  const cloudInputs = [
    { name: 'enabled', type: 'checkbox', checked: true },
    { name: 'target_cid', type: 'text', value: ' 67890 ' },
    { name: 'staging_dir', type: 'text', value: 'staging-reviewed' },
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
  assert.equal(navidromeSaved.staging_dir, 'staging-reviewed');
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
