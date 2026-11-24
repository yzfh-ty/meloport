const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'assets/app.js'), 'utf8');

test('restoring a browser session leaves the login form usable after logout', () => {
  const nodes = { 'login-fields': { disabled: true } };
  const state = { authenticated: false };
  let routed = false, refreshed = false;
  const context = { state, $: (id) => nodes[id], show() {}, route: () => { routed = true; }, refreshOverview: () => { refreshed = true; } };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('  function unlockSession()'), source.indexOf('  async function checkSession()')) + '\nthis.unlock = unlockSession;', context);
  context.unlock();
  assert.equal(nodes['login-fields'].disabled, false);
  assert.equal(state.authenticated, true);
  assert.ok(routed && refreshed);
});

test('locking an expired session hides management, stops polling and clears the password', () => {
  let aborted = 0, canceledCloud = false, cleared = false;
  const controller = { abort: () => { aborted++; } };
  const state = { authenticated: true, overview: { tasks: [{}] }, route: 'settings', overviewVersion: 3, overviewController: controller, previewController: controller,
    search: { controller }, rankings: { controller }, discovery: { controller }, audio: { controller }, cloud: { controller }, pollTimer: 123, overviewPromise: {} };
  const nodes = new Map();
  const node = (id) => { if (!nodes.has(id)) nodes.set(id, { value: 'previous-password', replaceChildren() {} }); return nodes.get(id); };
  const document = { title: '', querySelectorAll: () => [] };
  const context = { state, $: node, document, text: (id, text) => { node(id).text = text; }, show: (id, visible) => { node(id).hidden = !visible; },
    clearTimeout: () => { cleared = true; }, cancelCloudLogin: () => { canceledCloud = true; } };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('  function lockSession('), source.indexOf('  function unlockSession()')) + '\nthis.lock = lockSession;', context);
  context.lock('登录已过期');
  assert.equal(state.authenticated, false);
  assert.equal(state.overview, null);
  assert.equal(state.overviewVersion, 4);
  assert.equal(node('app-shell').hidden, true);
  assert.equal(node('login-view').hidden, false);
  assert.equal(node('login-password').value, '');
  assert.equal(node('login-error').text, '登录已过期');
  assert.equal(node('login-status').text, '');
  assert.equal(aborted, 7);
  assert.ok(cleared && canceledCloud);
});
