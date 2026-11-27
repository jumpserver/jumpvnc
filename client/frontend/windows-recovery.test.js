import test from 'node:test';
import assert from 'node:assert/strict';
import {
  authorizeRecoveredDesktop, displayProfile, DisplayTransportClosed, recoverWindowsDisplay,
  waitForSignal, windowsLoginTransition,
} from './windows-recovery.js';

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test('display reconnect takes only authenticated SVNC transport fields and the accepted VNC password', () => {
  const original = {
    host: 'example.test', port: 5922, user: 'transport', jump: 'jump', identity: 'key',
    knownHosts: 'hosts', hostKeyAlias: 'alias', vncUsername: 'rfb',
    windowsAccount: 'local-user', windowsPassword: 'example-windows-secret', storedWindowsPassword: true,
    trustHost: 'host', trustHostKey: 'fingerprint', storedSSHPassword: true, sshPassword: 'other',
    storedPassword: true, vncPassword: 'replaced',
  };
  const expected = {
    host: 'example.test', port: 5922, user: 'transport', jump: 'jump', identity: 'key',
    knownHosts: 'hosts', hostKeyAlias: 'alias', vncUsername: 'rfb', transport: 'subsystem',
    vncPassword: 'accepted-by-rfb',
  };
  assert.deepEqual(displayProfile(original, 'accepted-by-rfb'), expected);
  assert.deepEqual(displayProfile(original, ''), { ...expected, vncPassword: '' });
});

test('pending disconnect waits for the single login outcome; submitted and unconfirmed expire', async () => {
  let now = 10;
  for (const outcome of ['submitted', 'unconfirmed', 'active', 'unexpected']) {
    const request = deferred();
    const login = windowsLoginTransition(request.promise, () => now);
    assert.equal(login.mayRecover(), true);
    request.resolve(outcome);
    assert.equal(await login.result, outcome);
    assert.equal(login.mayRecover(), ['submitted', 'unconfirmed'].includes(outcome));
    assert.equal(login.mayRecover('Error decoding rect: invalid'), false);
    now += 20001;
    assert.equal(login.mayRecover(), false);
  }
  const login = windowsLoginTransition(Promise.reject(new Error('login refused')));
  await assert.rejects(login.result, /refused/);
  assert.equal(login.mayRecover(), false);
});

test('cancelling a pending login does not wait for or use its late result', async () => {
  const request = deferred();
  const controller = new AbortController();
  const pending = waitForSignal(request.promise, controller.signal);
  controller.abort();
  await assert.rejects(pending, { name: 'AbortError' });
  request.resolve('submitted');
});

function recovery(overrides) {
  let elapsed = 0;
  return recoverWindowsDisplay({
    profile: {}, signal: new AbortController().signal,
    now: () => elapsed, delay: async ms => { elapsed += ms; },
    open: async () => ({ id: 'new' }), attach: async () => {},
    close: async () => {}, ...overrides,
  });
}

test('a 60s Windows service restart plus startup time can recover the same display', async () => {
  let elapsed = 0, target;
  const profile = displayProfile({ host: 'example.test' }, 'rfb');
  const attempts = [];
  await recovery({
    profile, now: () => elapsed, delay: async ms => { elapsed += ms; },
    open: async p => {
      assert.equal(p, profile);
      attempts.push(elapsed);
      if (elapsed < 66000) throw new Error('连接 Agent: dial tcp: connection refused');
      return { id: 'after-restart' };
    },
    attach: async (connection, remaining) => {
      assert.equal(remaining, 80000 - elapsed);
      target = connection.id;
    },
  });
  assert.equal(target, 'after-restart');
  assert.equal(attempts[0], 0);
  assert.ok(attempts.at(-1) >= 66000 && attempts.at(-1) < 80000);
});

test('at 80s the retry window ends even if the server stays unavailable or a pending dial succeeds late', async () => {
  for (const slowDial of [false, true]) {
    let elapsed = 0, attempts = 0, lastDial;
    await assert.rejects(recovery({
      now: () => elapsed, delay: async ms => { elapsed += ms; },
      open: async () => {
        lastDial = elapsed;
        attempts++;
        if (slowDial && elapsed >= 70000) { elapsed += 12000; return {}; }
        throw new Error('SSH: EOF');
      },
      attach: () => assert.fail('must not attach after the deadline'),
    }), /暂时不可用/);
    assert.ok(elapsed >= 80000 && lastDial < 80000);
    assert.ok(attempts > 1 && attempts <= 15);
  }
});

test('cancelling during the long SCM restart wait prevents all later dials', async () => {
  let elapsed = 0, lastDial;
  const controller = new AbortController();
  await assert.rejects(recovery({
    now: () => elapsed, signal: controller.signal,
    delay: async ms => { elapsed += ms; if (elapsed > 60000) controller.abort(); },
    open: async () => { lastDial = elapsed; throw new Error('SSH: EOF'); },
    attach: () => assert.fail('cancelled recovery cannot attach'),
  }), { name: 'AbortError' });
  assert.ok(elapsed > 60000 && lastDial <= 60000);
});

test('an interrupted display handshake is closed before opening another', async () => {
  const events = [];
  await recovery({
    open: async () => { events.push('open'); return {}; },
    attach: async () => { events.push('attach'); if (events.length === 2) throw new DisplayTransportClosed(); },
    close: async () => { events.push('close'); },
  });
  assert.deepEqual(events, ['open', 'attach', 'close', 'open', 'attach']);
});

test('new trust, credentials, or account-verification errors stop after one attempt', async () => {
  for (const challenge of [{ hostKey: {} }, { auth: { fields: ['vncPassword'] } }]) {
    let opens = 0;
    await assert.rejects(recovery({
      open: async () => { opens++; return challenge; }, attach: () => assert.fail('unexpected display'),
    }), /手动连接/);
    assert.equal(opens, 1);
  }
  for (const error of ['unable to authenticate', 'host key changed: EOF']) {
    let opens = 0;
    await assert.rejects(recovery({ open: async () => { opens++; throw new Error(error); } }), new RegExp(error));
    assert.equal(opens, 1);
  }
  let opens = 0;
  await assert.rejects(recovery({
    open: async () => { opens++; return {}; }, attach: async () => { throw new Error('account mismatch'); },
  }), /account mismatch/);
  assert.equal(opens, 1);
});

test('a cancelled dial cannot attach or start another connection', async () => {
  const controller = new AbortController();
  let attached = false;
  await assert.rejects(recovery({
    signal: controller.signal,
    open: async () => { controller.abort(); return {}; },
    attach: async () => { attached = true; },
  }), { name: 'AbortError' });
  assert.equal(attached, false);
});

test('recovered RFB is gated on one positive account check after RFB authentication', async () => {
  const rfb = new EventTarget();
  const answer = deferred();
  let calls = 0;
  const ready = authorizeRecoveredDesktop(rfb, new AbortController().signal, async () => {
    calls++; return answer.promise;
  });
  assert.equal(calls, 0);
  assert.equal(rfb.viewOnly, true);
  rfb.dispatchEvent(new Event('connect'));
  rfb.dispatchEvent(new Event('connect'));
  assert.equal(calls, 1);
  assert.equal(rfb.viewOnly, true);
  answer.resolve(true);
  await ready;
  assert.equal(rfb.viewOnly, false);
});

test('wrong account, another challenge, cancellation, or socket closure never enables the recovered desktop', async () => {
  for (const event of ['inactive', 'api-error', 'securityfailure', 'credentialsrequired', 'disconnect', 'abort']) {
    const rfb = new EventTarget();
    const controller = new AbortController();
    const answer = deferred();
    const ready = authorizeRecoveredDesktop(rfb, controller.signal, () => answer.promise, 50);
    rfb.dispatchEvent(new Event('connect'));
    if (event === 'inactive') answer.resolve(false);
    else if (event === 'api-error') answer.reject(new Error('desktop closed'));
    else if (event === 'abort') controller.abort();
    else rfb.dispatchEvent(new Event(event));
    await assert.rejects(ready, error => (error instanceof DisplayTransportClosed) === (event === 'disconnect'));
    answer.resolve(true);
    await Promise.resolve();
    assert.equal(rfb.viewOnly, true);
  }
});

test('a console change during account verification requires a fresh authenticated display', async () => {
  const oldDisplay = new EventTarget();
  const newDisplay = new EventTarget();
  const oldCheck = deferred();
  const signal = new AbortController().signal;
  const displays = [oldDisplay, newDisplay];
  let closed = 0;
  await recovery({
    signal,
    open: async () => displays.shift(),
    close: async () => { closed++; },
    attach: async display => {
      const isOld = display === oldDisplay;
      const ready = authorizeRecoveredDesktop(display, signal, () => isOld ? oldCheck.promise : true);
      display.dispatchEvent(new Event('connect'));
      if (isOld) {
        display.failureReason = 'Unexpected server disconnect';
        display.dispatchEvent(new Event('disconnect'));
        oldCheck.resolve(true);
      }
      await ready;
    },
  });
  assert.equal(closed, 1);
  assert.equal(oldDisplay.viewOnly, true);
  assert.equal(newDisplay.viewOnly, false);
  assert.equal(displays.length, 0);
});

test('a submitted login keeps input disabled while Windows is switching accounts', async () => {
  const rfb = new EventTarget();
  const answers = [false, true];
  const ready = authorizeRecoveredDesktop(rfb, new AbortController().signal, async () => {
    assert.equal(rfb.viewOnly, true);
    return answers.shift();
  });
  rfb.dispatchEvent(new Event('connect'));
  await ready;
  assert.deepEqual(answers, []);
  assert.equal(rfb.viewOnly, false);
});

test('a VNC negotiation error reported as disconnect stops before retry or verification', async () => {
  const rfb = new EventTarget();
  rfb.failureReason = 'Unsupported security types (types: 19)';
  const ready = authorizeRecoveredDesktop(rfb, new AbortController().signal, () => assert.fail('not authenticated'));
  rfb.dispatchEvent(new Event('disconnect'));
  await assert.rejects(ready, /Unsupported security types/);
  assert.equal(rfb.viewOnly, true);
});
