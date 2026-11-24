import assert from 'node:assert/strict';
import { syncTransport, setProfile, profileFromForm, desktopEndpoint } from './transport.js';

function field(value = '') {
  let text = String(value);
  return { get value() { return text; }, set value(v) { text = String(v); }, checked: false };
}
function fixture() {
  const elements = Object.fromEntries(['protocol', 'transport', 'host', 'port', 'user', 'sshTunnel', 'sshHost', 'sshPort', 'sshUser',
    'jump', 'identity', 'knownHosts', 'hostKeyAlias', 'storedPassword', 'vncPassword', 'sshPassword', 'storedSSHPassword'].map(name => [name, field()]));
  elements.sshTunnel.type = elements.storedPassword.type = elements.storedSSHPassword.type = 'checkbox';
  const rows = Object.fromEntries(['data-vnc', 'data-ssh-tunnel', 'data-user', 'data-ssh'].map(name => [name, { hidden: false }]));
  return { elements, rows, dataset: {}, querySelectorAll: selector => [rows[selector.slice(1, -1)]] };
}
const form = fixture();
const p = { transport: 'subsystem', host: 'desktop', port: 5922, user: 'vncuser', jump: '',
  identity: '/custom/client', knownHosts: '/custom/known_hosts', hostKeyAlias: 'pinned-desktop', vncPort: 0, storedPassword: true };
setProfile(form, p);
assert.equal(form.elements.protocol.value, 'svnc');
assert.equal(form.rows['data-vnc'].hidden, true);
assert.equal(form.rows['data-ssh-tunnel'].hidden, true);
assert.equal(form.elements.sshHost.required, false);
assert.equal(form.elements.sshHost.disabled, true);
assert.equal(form.rows['data-user'].hidden, false);
assert.equal(form.rows['data-ssh'].hidden, false);
assert.deepEqual(profileFromForm(form), { ...p, vncHost: '', vncPassword: '', vncUsername: '', sshPassword: '', storedSSHPassword: false });

// Selecting VNC resets the protocol port and needs no SSH credentials.
form.elements.protocol.value = 'vnc';
syncTransport(form);
assert.equal(form.elements.port.value, '5900');
assert.equal(form.rows['data-vnc'].hidden, false);
assert.equal(form.rows['data-ssh-tunnel'].hidden, true);
assert.equal(form.rows['data-user'].hidden, true);
assert.equal(form.rows['data-ssh'].hidden, true);
let plain = profileFromForm(form);
assert.equal(plain.transport, 'vnc');
assert.equal(plain.port, 5900);
assert.equal(plain.identity, '');
assert.equal(plain.user, '');
assert.equal(plain.storedPassword, true);

form.elements.sshTunnel.checked = true;
syncTransport(form);
assert.equal(form.rows['data-ssh'].hidden, false);
assert.equal(form.rows['data-ssh-tunnel'].hidden, false);
assert.equal(form.rows['data-user'].hidden, true);
assert.equal(form.elements.sshUser.disabled, false);
assert.equal(form.elements.sshHost.disabled, false);
assert.equal(form.elements.sshHost.required, true);
form.elements.host.value = 'private-desktop';
form.elements.port.value = '5905';
form.elements.sshHost.value = 'bastion';
form.elements.sshPort.value = '2222';
let tunnel = profileFromForm(form);
assert.equal(tunnel.transport, 'forward');
assert.equal(tunnel.host, 'bastion');
assert.equal(tunnel.port, 2222);
assert.equal(tunnel.vncHost, 'private-desktop');
assert.equal(tunnel.vncPort, 5905);

// Existing forward profiles load as VNC + SSH tunnel, preserving every endpoint.
setProfile(form, { ...p, transport: 'forward', host: 'old-agent', port: 2225, vncPort: 5905 });
assert.equal(form.elements.host.value, '127.0.0.1');
assert.equal(form.elements.port.value, '5905');
assert.equal(form.elements.sshHost.value, 'old-agent');
assert.equal(form.elements.sshPort.value, '2225');
assert.equal(form.elements.sshTunnel.checked, true);
assert.equal(profileFromForm(form).vncPort, 5905);
setProfile(form, tunnel);
assert.deepEqual(profileFromForm(form), tunnel);
setProfile(form, plain);
assert.deepEqual(profileFromForm(form), plain);
form.elements.protocol.value = 'svnc';
syncTransport(form);
assert.equal(form.elements.port.value, '5922');
assert.equal(form.rows['data-ssh-tunnel'].hidden, true);
assert.equal(form.elements.sshHost.required, false);
assert.equal(form.elements.sshPort.disabled, true);

assert.equal(desktopEndpoint(p), 'vncuser@desktop · SVNC 5922');
assert.equal(desktopEndpoint(plain), 'desktop · VNC 5900');
assert.equal(desktopEndpoint(tunnel), 'vncuser@bastion · SSH 2222 → VNC private-desktop:5905');
