const normalize = text => text.replace(/\r\n|\r/g, '\n');

// Serialize native reads/writes so a delayed read cannot echo a remote update.
export function clipboardSync({ read, write, send, changed = () => {}, report = () => {} }) {
  let stopped = false;
  let lastText;
  let queue = Promise.resolve();
  const enqueue = action => {
    const next = queue.then(() => stopped ? undefined : action());
    queue = next.catch(() => {});
    return next;
  };
  return {
    sync(force = false) {
      return enqueue(async () => {
        const value = await read();
        if (stopped || !value.focused || !value.hasText) return;
        const text = normalize(value.text);
        if (!force && text === lastText) return;
        send(text);
        lastText = text;
        changed(text);
        report('已同步本机文本到远端剪贴板');
      });
    },
    receive(value) {
      return enqueue(async () => {
        const text = normalize(value);
        changed(text);
        if (await write(text) && !stopped) {
          lastText = text;
          report('已同步远端文本到本机剪贴板');
        }
      });
    },
    send(value) {
      return enqueue(async () => {
        const text = normalize(value);
        await write(text);
        if (stopped) return;
        send(text);
        lastText = text;
        report('已发送到远端剪贴板，请在远端应用中粘贴');
      });
    },
    stop() { stopped = true; },
  };
}
