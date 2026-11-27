export function missingCredentials(profile) {
  const missing = [];
  if (profile.transport !== 'vnc' && !profile.user.trim()) missing.push('user');
  if (profile.transport === 'subsystem' && profile.windowsAccount && !profile.storedWindowsPassword && !profile.windowsPassword) missing.push('windowsPassword');
  if (!profile.storedPassword && !profile.vncPassword) missing.push('vncPassword');
  return missing;
}

export function applyCredentials(profile, values) {
  Object.assign(profile, values);
  if (Object.hasOwn(values, 'user')) profile.user = profile.user.trim();
  if (Object.hasOwn(values, 'vncPassword')) profile.storedPassword = false;
  if (Object.hasOwn(values, 'sshPassword')) profile.storedSSHPassword = false;
  return profile;
}

export function profileToSave(profile, values = {}) {
  const next = applyCredentials({ ...profile }, values);
  next.storedPassword = !!next.vncPassword || profile.storedPassword;
  next.storedSSHPassword = !!next.sshPassword || profile.storedSSHPassword;
  if (!next.storedWindowsPassword) next.windowsPassword = '';
  return next;
}

let activeCancel;

export function cancelCredentials() { activeCancel?.(); }

export async function promptCredentials(profile, fields, message, onSave) {
  const dialog = document.getElementById('credentials-dialog');
  const form = document.getElementById('credentials-form');
  const save = document.getElementById('credentials-save');
  form.querySelectorAll('button').forEach(button => { button.disabled = false; });
  save.textContent = '保存';
  save.hidden = !onSave || (fields.length === 1 && fields[0] === 'windowsPassword' && !profile.storedWindowsPassword);
  document.getElementById('credentials-message').textContent = message || (fields.includes('windowsPassword')
    ? `登录 ${profile.windowsAccount}` : '请填写连接所需的账号和密码。');
  for (const row of form.querySelectorAll('[data-credential]')) {
    const name = row.dataset.credential;
    const input = form.elements[name];
    const show = fields.includes(name);
    row.hidden = !show;
    input.disabled = !show;
    input.required = show;
    input.value = name === 'user' || name === 'vncUsername' ? profile[name] || '' : '';
  }
  let values;
  let finished = false;
  let onCancel;
  const closed = new Promise(resolve => {
    const finish = accepted => {
      if (finished) return;
      finished = true;
      if (accepted) values = Object.fromEntries(new FormData(form));
      if (dialog.open) dialog.close();
      resolve(accepted);
    };
    activeCancel = () => finish(false);
    form.onsubmit = event => { event.preventDefault(); finish(true); };
    onCancel = event => { event.preventDefault(); finish(false); };
    dialog.addEventListener('cancel', onCancel);
    dialog.addEventListener('close', activeCancel, { once: true });
    document.getElementById('credentials-cancel').onclick = activeCancel;
  });
  let saving = false;
  save.onclick = async () => {
    if (saving) return;
    const values = Object.fromEntries(new FormData(form));
    const next = profileToSave(profile, values);
    saving = true;
    const controls = [...form.querySelectorAll('input, button')];
    const disabled = controls.map(input => input.disabled);
    controls.forEach(input => { input.disabled = true; });
    save.textContent = '保存中…';
    try {
      const server = await onSave(next);
      const windowsPassword = fields.includes('windowsPassword') ? values.windowsPassword : profile.windowsPassword;
      Object.assign(profile, server.profile, { vncPassword: next.vncPassword, sshPassword: next.sshPassword, windowsPassword });
      if (dialog.open) document.getElementById('credentials-message').textContent = '已保存，可以继续连接。';
    } catch (err) {
      if (dialog.open) document.getElementById('credentials-message').textContent = err?.message || String(err);
    } finally {
      saving = false;
      if (!finished) {
        save.textContent = '保存';
        controls.forEach((input, i) => { input.disabled = disabled[i]; });
      }
    }
  };
  let accepted;
  try {
    dialog.showModal();
    form.querySelector('input:not([disabled])')?.focus();
    accepted = await closed;
  } finally {
    dialog.removeEventListener('close', activeCancel);
    dialog.removeEventListener('cancel', onCancel);
    activeCancel = undefined;
    form.onsubmit = null;
    save.onclick = null;
    document.getElementById('credentials-cancel').onclick = null;
    form.reset();
  }
  if (!accepted) return false;
  applyCredentials(profile, values);
  return true;
}
