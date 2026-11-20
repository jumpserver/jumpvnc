import test from 'node:test';
import assert from 'node:assert/strict';
import { clipboardSync } from './clipboard.js';

function fixture() {
  const local = { focused: true, hasText: true, text: '本机 → remote 😀' };
  const sent = [], written = [];
  const sync = clipboardSync({
    read: async () => ({ ...local }),
    write: async text => { if (!local.focused) return false; written.push(text); local.text = text; return true; },
    send: text => sent.push(text), changed() {}, report() {},
  });
  return { local, sent, written, sync };
}

test('local text syncs once, force sync precedes paste, remote text never echoes', async () => {
  const { local, sent, written, sync } = fixture();
  await sync.sync();
  await sync.sync();
  assert.deepEqual(sent, ['本机 → remote 😀']);
  await sync.sync(true);
  assert.equal(sent.length, 2);
  await sync.receive('远端 → 本机\r\n第二行 😀');
  await sync.sync();
  assert.equal(sent.length, 2);
  assert.deepEqual(written, ['远端 → 本机\n第二行 😀']);
  assert.equal(local.text, written[0]);
});

test('background viewers and non-text clipboards do not clear or send text', async () => {
  const { local, sent, written, sync } = fixture();
  local.focused = false;
  await sync.sync();
  await sync.receive('background');
  assert.deepEqual(sent, []);
  assert.deepEqual(written, []);
  local.focused = true;
  local.hasText = false;
  await sync.sync();
  assert.deepEqual(sent, []);
  local.hasText = true;
  local.text = '';
  await sync.sync();
  assert.deepEqual(sent, ['']);
});

test('manual send updates the local clipboard so polling cannot undo it', async () => {
  const { sent, written, sync } = fixture();
  await sync.send('手动文本');
  await sync.sync();
  assert.deepEqual(sent, ['手动文本']);
  assert.deepEqual(written, ['手动文本']);
});

test('disconnect cancels reads and queued writes from the previous connection', async () => {
  let finishRead;
  const sent = [], written = [];
  const sync = clipboardSync({ read: () => new Promise(resolve => { finishRead = resolve; }),
    write: async text => written.push(text), send: text => sent.push(text), changed() {}, report() {} });
  const read = sync.sync();
  await Promise.resolve();
  sync.stop();
  finishRead({ focused: true, hasText: true, text: 'old' });
  await read;
  await sync.receive('old remote');
  assert.deepEqual(sent, []);
  assert.deepEqual(written, []);
});
