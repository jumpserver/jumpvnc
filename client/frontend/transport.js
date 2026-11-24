const defaultPorts = { svnc: 5922, vnc: 5900 };

export function syncTransport(form) {
  const fields = form.elements;
  const protocol = fields.protocol.value;
  if (form.dataset.protocol && form.dataset.protocol !== protocol) fields.port.value = defaultPorts[protocol];
  form.dataset.protocol = protocol;
  fields.port.placeholder = String(defaultPorts[protocol]);
  const vnc = protocol === 'vnc';
  const tunnel = vnc && fields.sshTunnel.checked;
  fields.transport.value = vnc ? tunnel ? 'forward' : 'vnc' : 'subsystem';
  for (const row of form.querySelectorAll('[data-vnc]')) row.hidden = !vnc;
  for (const row of form.querySelectorAll('[data-ssh-tunnel]')) row.hidden = !tunnel;
  for (const row of form.querySelectorAll('[data-ssh]')) row.hidden = vnc && !tunnel;
  for (const row of form.querySelectorAll('[data-user]')) row.hidden = vnc;
  fields.sshHost.disabled = !tunnel;
  fields.sshHost.required = tunnel;
  fields.sshPort.disabled = !tunnel;
  fields.sshPort.required = tunnel;
  fields.sshUser.disabled = !tunnel;
  fields.sshUser.required = false;
}

export function setProfile(form, profile) {
  const fields = form.elements;
  for (const [key, value] of Object.entries(profile)) {
    const field = fields[key];
    if (!field) continue;
    if (field.type === 'checkbox') field.checked = value;
    else field.value = value;
  }
  const forward = !profile.transport || profile.transport === 'forward';
  fields.protocol.value = forward || profile.transport === 'vnc' ? 'vnc' : 'svnc';
  fields.sshTunnel.checked = forward;
  fields.host.value = forward ? profile.vncHost || '127.0.0.1' : profile.host;
  fields.port.value = forward ? profile.vncPort || 5900 : profile.port || defaultPorts[fields.protocol.value];
  fields.sshHost.value = forward ? profile.host : '';
  fields.sshPort.value = forward ? profile.port : 22;
  fields.sshUser.value = profile.user || '';
  form.dataset.protocol = fields.protocol.value;
  syncTransport(form);
}

export function profileFromForm(form) {
  const f = form.elements;
  const protocol = f.protocol.value;
  const tunnel = protocol === 'vnc' && f.sshTunnel.checked;
  const plain = protocol === 'vnc' && !tunnel;
  return {
    transport: tunnel ? 'forward' : plain ? 'vnc' : 'subsystem',
    host: (tunnel ? f.sshHost.value : f.host.value).trim(),
    port: Number(tunnel ? f.sshPort.value : f.port.value) || (tunnel ? 22 : defaultPorts[protocol]),
    user: plain ? '' : (tunnel ? f.sshUser.value : f.user.value).trim(),
    vncHost: tunnel ? f.host.value.trim() : '',
    vncPort: tunnel ? Number(f.port.value) || 5900 : 0,
    jump: plain ? '' : f.jump.value.trim(),
    identity: plain ? '' : f.identity.value.trim(),
    knownHosts: plain ? '' : f.knownHosts.value.trim(),
    hostKeyAlias: plain ? '' : f.hostKeyAlias.value.trim(),
    storedPassword: f.storedPassword.checked,
    vncPassword: f.vncPassword?.value || '',
    vncUsername: f.vncUsername?.value || '',
    sshPassword: tunnel ? f.sshPassword?.value || '' : '',
    storedSSHPassword: tunnel && !!f.storedSSHPassword?.checked,
  };
}

export function desktopEndpoint(profile) {
  if (profile.transport === 'vnc') return `${profile.host} · VNC ${profile.port}`;
  const address = `${profile.user}@${profile.host}`;
  return profile.transport === 'subsystem'
    ? `${address} · SVNC ${profile.port}`
    : `${address} · SSH ${profile.port} → VNC ${profile.vncHost || '127.0.0.1'}:${profile.vncPort}`;
}
