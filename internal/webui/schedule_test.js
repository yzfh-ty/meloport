const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, 'assets', 'app.js'), 'utf8');
const start = source.indexOf('// Schedule helpers are kept pure');
const end = source.indexOf('// End schedule helpers.');
assert.ok(start >= 0 && end > start, 'pure schedule helpers must be available for direct tests');
const context = {};
vm.runInNewContext(`${source.slice(start, end)}\nthis.helpers = scheduleUI;`, context);
const { decode, encode, describe } = context.helpers;

test('daily, working-day and weekly plans round trip with readable summaries', () => {
  for (const [value, mode, label] of [
    ['0 3 * * *', 'daily', '每天 03:00'],
    ['45 23 * * *', 'daily', '每天 23:45'],
    ['15 8 * * 1-5', 'weekdays', '周一至周五 08:15'],
    ['30 9 * * 1', 'weekly', '每周一 09:30'],
    ['0 0 * * 0', 'weekly', '每周日 00:00'],
  ]) {
    assert.equal(decode(value).mode, mode, value);
    assert.equal(encode(decode(value)), value, value);
    assert.equal(describe(value), label, value);
  }
  assert.equal(describe('0 0 * * 7'), '每周日 00:00');
  assert.equal(describe('15 8 * * MON-FRI'), '周一至周五 08:15');
  assert.equal(describe('30 9 * * TUE'), '每周二 09:30');
});

test('all offered intervals produce anchored, evenly spaced schedules', () => {
  for (const interval of [1, 2, 3, 4, 6, 8, 12]) {
    const value = encode({ mode: 'hours', interval });
    assert.equal(value, `0 */${interval} * * *`);
    assert.equal(decode(value).mode, 'hours');
    assert.equal(Number(decode(value).interval), interval);
    assert.equal(describe(value), `每 ${interval} 小时（整点）`);
  }
  for (const interval of [5, 10, 15, 20, 30]) {
    const value = encode({ mode: 'minutes', interval });
    assert.equal(value, `*/${interval} * * * *`);
    assert.equal(decode(value).mode, 'minutes');
    assert.equal(Number(decode(value).interval), interval);
  }
  assert.equal(decode('0 * * * *').mode, 'hours');
  assert.equal(describe('0 * * * *'), '每 1 小时（整点）');
});

test('unsupported plans preserve the exact original expression without exposing it', () => {
  for (const value of [
    '15 */2 * * *',       // A non-zero minute must not move to the hour.
    '0 1-23/2 * * *',     // Odd-hour anchors must not become even hours.
    '0 0/2 * * *',
    '*/7 * * * *',        // Uneven boundaries cannot be represented as the offered intervals.
    '0 */5 * * *',
    '0 */24 * * *',
    '0 3 1 * *',
    '0 3 * * 1,3,5',
    '0 3 * JAN MON',
    '  10 4 1-10 * *  ',
  ]) {
    const model = decode(value);
    assert.equal(model.mode, 'custom', value);
    assert.equal(encode(model), value, value);
    assert.equal(describe(value), '已有自定义计划，保持不变');
    assert.ok(!describe(value).includes(value.trim()), value);
  }
});

test('inherit is explicit and invalid selections are rejected', () => {
  assert.equal(decode('', true).mode, 'inherit');
  assert.equal(encode(decode('', true)), '');
  assert.equal(describe('', true), '继承默认计划');
  assert.equal(decode('').mode, 'custom');
  assert.throws(() => encode({ mode: 'daily', time: '' }), /同步时间/);
  assert.throws(() => encode({ mode: 'daily', time: '24:00' }), /同步时间/);
  assert.throws(() => encode({ mode: 'weekly', time: '03:00', weekday: '8' }), /星期/);
  assert.throws(() => encode({ mode: 'hours', interval: '5' }), /同步计划/);
  assert.throws(() => encode({ mode: 'minutes', interval: '7' }), /同步计划/);
});

test('replacing a preserved custom plan requires an explicit supported model', () => {
  const model = decode('15 */2 * * *');
  assert.equal(encode(model), '15 */2 * * *');
  assert.equal(encode({ ...model, mode: 'daily', time: '04:25' }), '25 4 * * *');
});
