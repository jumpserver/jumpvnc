// A successful Windows logon can move the desktop to the console and restart
// the display service. Only the original login request can arm this recovery.
export function windowsLoginTransition(request, now = Date.now) {
  let expires = Infinity;
  const result = request.then(value => {
    expires = value === 'submitted' || value === 'unconfirmed' ? now() + 20_000 : -Infinity;
    return value;
  }, error => {
    expires = -Infinity;
    throw error;
  });
  return { result, mayRecover: failure => now() <= expires && socketClosed(failure) };
}

export function displayProfile(profile, password) {
  const { host, port, user, jump, identity, knownHosts, hostKeyAlias, vncUsername } = profile;
  return { transport: 'subsystem', host, port, user, jump, identity, knownHosts, hostKeyAlias,
    vncUsername, vncPassword: password || '' };
}

export function waitForSignal(operation, signal) {
  if (signal.aborted) return Promise.reject(signal.reason);
  return new Promise((resolve, reject) => {
    const cancel = () => reject(signal.reason);
    signal.addEventListener('abort', cancel, { once: true });
    Promise.resolve(operation).then(resolve, reject).finally(() => signal.removeEventListener('abort', cancel));
  });
}

async function wait(milliseconds, signal) {
  let timer;
  try {
    await waitForSignal(new Promise(resolve => { timer = setTimeout(resolve, milliseconds); }), signal);
  } finally {
    clearTimeout(timer);
  }
}

export class DisplayTransportClosed extends Error {}

function socketClosed(failure) {
  return !failure || /^(?:Connection closed|Unexpected server disconnect)\b/.test(failure);
}

// A new console may show a different user. Keep noVNC input disabled and the
// screen hidden in the caller until the authenticated SSH channel confirms it.
export function authorizeRecoveredDesktop(rfb, signal, verify, remaining = 12000) {
  rfb.viewOnly = true;
  return new Promise((resolve, reject) => {
    let settled = false;
    let verifying = false;
    function finish(error) {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal.removeEventListener('abort', cancel);
      for (const [event, handler] of Object.entries(handlers)) rfb.removeEventListener(event, handler);
      if (error) reject(error);
      else {
        rfb.viewOnly = false;
        resolve();
      }
    }
    const cancel = () => finish(signal.reason || new Error('已取消恢复'));
    const handlers = {
      async connect() {
        if (verifying) return;
        verifying = true;
        try {
          if (await verify() !== true) throw new Error('无法确认此 Windows 账号已解锁。');
          finish();
        } catch (error) {
          finish(new Error('桌面账号确认失败：' + (error?.message || String(error))));
        }
      },
      securityfailure: () => finish(new Error('VNC 认证失败，请手动连接。')),
      credentialsrequired: () => finish(new Error('VNC 要求重新认证，请手动连接。')),
      disconnect() {
        if (verifying) finish(new Error('确认账号时桌面连接已中断，请手动连接。'));
        else if (socketClosed(rfb.failureReason)) finish(new DisplayTransportClosed());
        else finish(new Error('无法恢复 VNC 桌面：' + rfb.failureReason));
      },
    };
    const timer = setTimeout(() => finish(verifying
      ? new Error('确认 Windows 桌面账号超时，请手动连接。') : new DisplayTransportClosed()), Math.min(12000, remaining));
    for (const [event, handler] of Object.entries(handlers)) rfb.addEventListener(event, handler);
    signal.addEventListener('abort', cancel, { once: true });
    if (signal.aborted) cancel();
  });
}

function retryableDial(error) {
  // Only retry recognizable transport failures. A trust/authentication error
  // can include arbitrary text supplied by the peer and must stay terminal.
  const text = error?.message || String(error);
  if (/host key|fingerprint|authenticat|known_hosts|密码|指纹|主机密钥|hostkey/i.test(text)) return false;
  return /(?:connection refused|connection reset by peer|broken pipe|i\/o timeout|context deadline exceeded|unexpected EOF|: EOF)\s*$/i.test(text);
}

export async function recoverWindowsDisplay({ profile, open, attach, close, signal, delay = wait, now = () => performance.now() }) {
  // SCM waits 5s, 15s, then 60s on later failures within its 24h reset period.
  // Leave 20s after its longest delay for the service and desktop to start.
  const deadline = now() + 80_000;
  let pause = 0;
  while (now() < deadline) {
    await delay(Math.min(pause, deadline - now()), signal);
    if (signal.aborted) throw signal.reason;
    if (now() >= deadline) break;
    pause = Math.min(pause ? pause * 2 : 1000, 8000);
    let connection;
    try {
      connection = await open(profile);
    } catch (error) {
      if (!signal.aborted && retryableDial(error)) continue;
      throw error;
    }
    if (signal.aborted) throw signal.reason;
    if (connection.hostKey) throw new Error('服务器身份需要重新确认。请手动连接并核对 SSH 指纹。');
    if (connection.auth) throw new Error('服务器要求重新认证。请手动连接。');
    const remaining = deadline - now();
    if (remaining <= 0) break;
    try {
      await attach(connection, remaining);
      return;
    } catch (error) {
      if (!(error instanceof DisplayTransportClosed) || signal.aborted) throw error;
      await close();
    }
  }
  throw new Error('Windows 桌面服务暂时不可用，请稍后重新连接。');
}
