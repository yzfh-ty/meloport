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
    { name: 'media_listen', type: 'text', value: ' 127.0.0.1:18081 ', disabled: true },
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
  assert.equal(settings.media_listen, '127.0.0.1:18081');
  assert.equal(settings.navidrome_url, 'http://127.0.0.1:4533');
  assert.equal(settings.navidrome_mount_root, '/music');
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
