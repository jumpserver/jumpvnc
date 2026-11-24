import { syncTransport, setProfile, profileFromForm, desktopEndpoint } from './transport.js';
import { missingCredentials, promptCredentials, cancelCredentials, profileToSave } from './auth.js';
import RFB from '@novnc/novnc';
import { encodings } from './node_modules/@novnc/novnc/core/encodings.js';
import { supportsWebCodecsH264Decode } from './node_modules/@novnc/novnc/core/util/browser.js';
import { H264Parser } from './node_modules/@novnc/novnc/core/decoders/h264.js';
import { installH264AccessUnits } from './h264-access-units.js';
import { applyQuality, NetworkWindow, qualityProfiles } from './performance.js';
import { measureRFB } from './rfb-performance.js';
import { nativeRFB } from './native-rfb.js';
import { videoRFB } from './video-rfb.js';
import { fileLocationPath, fileLocationNote } from './file-location.js';
import { clipboardSync } from './clipboard.js';

installH264AccessUnits(H264Parser);
const MeasuredRFB = measureRFB(nativeRFB(videoRFB(RFB, encodings, supportsWebCodecsH264Decode)));

const $ = (id) => document.getElementById(id);
const call = (method, ...args) => window.mygo.call('Viewer.' + method, ...args);
const form = $('connect-form');
const filesPanel = $('files-panel');
const list = $('file-list');

let client;
let connected = false;
let filesAvailable = false;
let connecting = false;
let disconnecting = false;
let profileReady = false;
let savingConnection = false;
let editingSnapshot;
let directory = '/';
let fileLocation;
let entries = [];
let selection;
let listRequest = 0;
let connectionRevision = 0;
let transferring = false;
let dragTimer;
let pendingDrop;
let stopPerformance;
let activePanel = null;
let serverName = '';
let clipboard;
let clipboardTimer;

function clipboardStatus(message, failed = false) {
  $('clipboard-status').textContent = message;
  $('clipboard-status').dataset.error = String(failed);
  $('clipboard-toggle').title = '文本剪贴板自动同步 · ' + message;
}

function clipboardError(error) { clipboardStatus(errorMessage(error), true); }

function stopClipboard() {
  clearTimeout(clipboardTimer);
  clipboard?.stop();
  clipboard = undefined;
}

function startClipboard(rfb, id) {
  stopClipboard();
  const sync = clipboard = clipboardSync({
    read: () => call('ReadClipboard', id),
    write: text => call('WriteClipboard', id, text),
    send: text => rfb.clipboardPasteFrom(text),
    changed: text => { $('clipboard-text').value = text; },
    report: clipboardStatus,
  });
  rfb.commandAsControl = /Mac|iPhone|iPad/.test(navigator.platform);
  rfb.beforePaste = () => sync.sync(true);
  rfb.onClipboardError = clipboardError;
  async function tick() {
    if (clipboard !== sync) return;
    try { await sync.sync(); } catch (error) { if (clipboard === sync) clipboardError(error); }
    if (clipboard === sync) clipboardTimer = setTimeout(tick, 250);
  }
  clipboardStatus('自动同步已开启');
  tick();
}

function showPanel(name) {
  activePanel = name;
  filesPanel.hidden = name !== 'files';
  $('settings-panel').hidden = name !== 'settings';
  $('files-toggle').setAttribute('aria-expanded', String(name === 'files'));
  $('settings-toggle').setAttribute('aria-expanded', String(name === 'settings'));
  if (name === 'files' && connected && filesAvailable) readDirectory(directory);
}

function selectQuality() {
  const name = $('quality-profile').value;
  const profile = qualityProfiles[name];
  if (client) applyQuality(client, name);
  $('quality-settings').textContent = profile.video
    ? (supportsWebCodecsH264Decode ? '优先 H.264 · 目标 6 Mbps / 30 FPS；不支持时使用流畅画质。实际编码见详细指标。'
      : '当前窗口不支持 H.264 解码，使用流畅画质。')
    : `请求画质 ${profile.quality}/9 · 压缩 ${profile.compression}/9`;
  $('header-quality').textContent = profile.label;
  $('header-quality').title = `画质请求：${profile.label}（Q${profile.quality}）`;
  try { localStorage.setItem('sshvnc.quality', name); } catch { /* Optional preference storage. */ }
}

function resetPerformance() {
  stopPerformance?.();
  stopPerformance = undefined;
  for (const element of document.querySelectorAll('[data-metric]')) element.textContent = '—';
  $('performance-panel').open = false;
  updateLatency(null);
}

function updateLatency(millis) {
  const indicator = $('status-indicator');
  const label = millis === null ? 'SSH 往返延迟采样中' : `SSH 往返延迟：${millis.toFixed(1)} ms`;
  indicator.dataset.level = millis === null ? '0' : millis <= 30 ? '4' : millis <= 80 ? '3' : millis <= 150 ? '2' : '1';
  indicator.title = label;
  indicator.setAttribute('aria-label', label);
  $('header-rtt').textContent = millis === null ? '—' : millis.toFixed(0) + ' ms';
}

function startPerformance(rfb, id) {
  stopPerformance?.();
  const network = new NetworkWindow();
  let stopped = false;
  let timer;
  stopPerformance = () => { stopped = true; clearTimeout(timer); };
  const active = () => !stopped && client === rfb && connected;
  const millis = (value) => value === null ? '—' : value.toFixed(2) + ' ms';
  async function tick() {
    if (!active()) return;
    const sample = rfb.performanceSnapshot();
    if (sample) {
      $('metric-updates').textContent = sample.updatesPerSecond.toFixed(1) + ' 次/秒';
      $('metric-encoding').textContent = sample.encodings.length
        ? sample.encodings.slice(0, 3).map(([name, count]) => `${name} ${count}块`).join(' · ')
        : sample.lastEncoding ? sample.lastEncoding + ' · 本次无更新' : '等待画面';
      $('metric-decode').textContent = millis(sample.decodeMillis);
      $('metric-paint').textContent = millis(sample.paintMillis);
      $('metric-wait').textContent = millis(sample.waitMillis);
      $('metric-queue').textContent = `${sample.queue} 项 · 峰值 ${sample.peakQueue}`;
      $('metric-backlog').textContent = formatSize(sample.receiveBacklog);
      $('metric-resolution').textContent = sample.width + ' × ' + sample.height;
      $('header-resolution').textContent = sample.width + ' × ' + sample.height;
      $('metric-cursor').textContent = !sample.cursorShapeUpdates ? '未收到形状'
        : sample.cursorWidth && sample.cursorHeight
          ? `本地 · ${sample.cursorWidth} × ${sample.cursorHeight}` : '隐藏';
    }
    try {
      const snapshot = await call('Performance', id);
      if (!active() || snapshot.id !== id) return;
      const rate = network.sample(snapshot);
      $('metric-bandwidth').textContent = rate ? rate.receiveMbps.toFixed(2) + ' Mbps' : '采样中…';
      $('metric-rtt').textContent = snapshot.rttMillis === null ? '—' : snapshot.rttMillis.toFixed(1) + ' ms';
      $('header-bandwidth').textContent = rate ? '↓ ' + rate.receiveMbps.toFixed(1) + ' Mbps' : '↓ —';
      updateLatency(snapshot.rttMillis);
      $('metric-received').textContent = formatSize(snapshot.receivedBytes);
    } catch {
      if (active()) {
        $('metric-bandwidth').textContent = '暂不可用';
        $('metric-rtt').textContent = '—';
        $('metric-received').textContent = '—';
        $('header-bandwidth').textContent = '↓ —';
        updateLatency(null);
      }
    } finally {
      if (active()) timer = setTimeout(tick, 1000);
    }
  }
  tick();
}

function status(message, state = '') {
  $('connection-status').textContent = message;
  $('status-indicator').dataset.state = state;
  $('status-indicator').title = message;
  $('state-title').textContent = message;
}

function showConnectionError(message) {
  $('connect-error').textContent = message;
  $('connect-error').hidden = !message;
  $('state-error').textContent = message;
  $('state-error').hidden = !message;
}

function errorMessage(error) {
  return error?.message || String(error);
}

function syncControls() {
  $('connect').disabled = !profileReady || savingConnection || connecting || connected || disconnecting;
  $('connect').textContent = connecting ? '连接中…' : '连接';
  $('state-actions').hidden = !profileReady || savingConnection || connecting || connected || disconnecting;
  $('disconnect').hidden = !connected && !connecting;
  $('disconnect').disabled = disconnecting;
  $('refresh').disabled = !connected || !filesAvailable;
  $('upload').disabled = !connected || !filesAvailable || transferring;
  $('download').disabled = !connected || !filesAvailable || transferring || !selection?.regular;
  $('mkdir').disabled = !connected || !filesAvailable || transferring;
  $('files-toggle').disabled = !connected || !filesAvailable;
  $('clipboard-toggle').disabled = !connected;
  $('send-secure-attention').disabled = !connected;
  $('settings-offline').hidden = connected || connecting;
  $('transfer-dot').hidden = !transferring;
  $('drop-label').textContent = connected && !filesAvailable ? '文件传输不可用' : transferring ? '正在传输文件' : '将文件拖到这里上传';
}

function listMessage(message) {
  const text = document.createElement('p');
  text.className = 'list-message';
  text.textContent = message;
  list.replaceChildren(text);
}

function showFilesOffline() {
  directory = '/';
  entries = [];
  selection = undefined;
  listRequest++;
  $('breadcrumbs').textContent = '/';
  fileLocation = undefined;
  $('file-location').hidden = true;
  listMessage('连接后显示远程文件');
  $('transfer-area').hidden = true;
  syncControls();
}

function connectOptions() {
  return profileFromForm(form);
}

async function saveProfile(profile) {
  const server = await call('SaveProfile', profileToSave(profile));
  serverName = server.name;
  $('connection-name').textContent = serverName;
  setProfile(form, server.profile);
  $('stored-row').hidden = !server.profile.storedPassword;
  $('state-endpoint').textContent = desktopEndpoint(server.profile);
  return server;
}

function showConnectionForm() {
  showConnectionError('');
  editingSnapshot = connectOptions();
  syncControls();
  $('connection-dialog').showModal();
}

function cancelConnectionForm() {
  if (editingSnapshot) setProfile(form, editingSnapshot);
  editingSnapshot = undefined;
  $('connection-dialog').close();
}

function updateCredentialFields(profile) {
  const user = profile.transport === 'forward' ? form.elements.sshUser : form.elements.user;
  user.value = profile.user;
  form.elements.vncPassword.value = profile.vncPassword || '';
  form.elements.sshPassword.value = profile.sshPassword || '';
  form.elements.vncUsername.value = profile.vncUsername || '';
  form.elements.storedPassword.checked = profile.storedPassword;
  form.elements.storedSSHPassword.checked = profile.storedSSHPassword;
}

async function connect(event) {
  event?.preventDefault();
  if (!profileReady || savingConnection || connecting || connected || disconnecting) return;
  if (!form.checkValidity()) {
    status('需要补充连接信息');
    if (!$('connection-dialog').open) showConnectionForm();
    form.reportValidity();
    return;
  }
  if ($('connection-dialog').open) {
    editingSnapshot = undefined;
    $('connection-dialog').close();
  }
  showConnectionError('');
  const profile = connectOptions();
  connecting = true;
  showPanel(null);
  $('connection-name').textContent = serverName || profile.jump || profile.host;
  syncControls();
  status(profile.transport === 'subsystem' ? '正在连接 SVNC' : '正在连接 VNC', 'connecting');
  try {
    const missing = missingCredentials(profile);
    if (missing.length) {
      if (!await promptCredentials(profile, missing, '', saveProfile)) { await disconnect(); return; }
      updateCredentialFields(profile);
    }
    let connection;
    while (connecting && !disconnecting) {
      connection = await call('Connect', profile);
      if (disconnecting || !connecting) return;
      if (connection.auth) {
        if (!await promptCredentials(profile, connection.auth.fields, connection.auth.message, saveProfile)) { await disconnect(); return; }
        updateCredentialFields(profile);
        continue;
      }
      if (!connection.hostKey) break;
      const identity = connection.hostKey;
      if (profile.trustHostKey) throw new Error('服务器身份发生变化，请重新连接并核对指纹');
      $('host-key-host').textContent = identity.host;
      $('host-key-fingerprint').textContent = `${identity.algorithm} · ${identity.fingerprint}`;
      const dialog = $('host-key-dialog');
      dialog.returnValue = '';
      const accepted = new Promise(resolve => dialog.addEventListener('close', () => resolve(dialog.returnValue === 'trust'), { once: true }));
      dialog.showModal();
      if (!await accepted || disconnecting || !connecting) {
        if (connecting) await disconnect();
        return;
      }
      profile.trustHostKey = identity.fingerprint;
    }
    if (disconnecting || !connecting) return;
    filesAvailable = connection.files;
    fileLocation = connection.fileLocation;
    updateFileLocation();
    status('正在连接桌面', 'connecting');
    $('vnc-screen').hidden = false;
    const rfb = new MeasuredRFB($('vnc-screen'), connection.url, {
      shared: true,
      credentials: connection.password ? { username: profile.vncUsername, password: connection.password } : {},
    });
    client = rfb;
    let desktopName = '';
    const updateEndpoint = () => {
      if (client !== rfb) return;
      const endpoint = desktopEndpoint(profile);
      $('desktop-host').title = `${profile.jump ? profile.jump + ' · ' : ''}${endpoint}${desktopName ? ' · ' + desktopName : ''}`;
      $('connection-endpoint').textContent = `${profile.jump ? profile.jump + ' → ' : ''}${endpoint}`;
      $('connection-backend').textContent = desktopName || '等待服务端桌面名称';
    };
    rfb.addEventListener('desktopname', ({ detail }) => {
      desktopName = detail.name;
      updateEndpoint();
    });
    applyQuality(rfb, $('quality-profile').value);
    rfb.scaleViewport = true;
    rfb.addEventListener('clipboard', ({ detail }) => {
      if (client !== rfb) return;
      if (clipboard) clipboard.receive(detail.text).catch(clipboardError);
      else $('clipboard-text').value = detail.text;
    });
    rfb.addEventListener('clipboardcapabilities', () => {
      if (client === rfb) clipboard?.sync(true).catch(clipboardError);
    });
    rfb.addEventListener('connect', () => {
      if (client !== rfb) return;
      connecting = false;
      connected = true;
      $('connection-metrics').hidden = false;
      updateEndpoint();
      $('connection-view').hidden = true;
      form.elements.vncPassword.value = '';
      form.elements.sshPassword.value = '';
      status('已连接', 'connected');
      rfb.focus();
      syncControls();
      listMessage(filesAvailable ? '打开文件管理后显示远程文件' : '文件传输不可用，桌面仍可正常使用');
      startPerformance(rfb, connection.id);
      startClipboard(rfb, connection.id);
    });
    rfb.addEventListener('securityfailure', () => {
      if (client === rfb) {
        form.elements.vncPassword.value = '';
        form.elements.storedPassword.checked = false;
        disconnect('VNC 认证失败，请重新填写密码');
      }
    });
    rfb.addEventListener('credentialsrequired', async ({ detail }) => {
      if (client !== rfb) return;
      const fields = detail.types.map(type => ({ username: 'vncUsername', password: 'vncPassword' })[type]).filter(Boolean);
      if (!fields.length) { await disconnect('服务器要求的认证方式暂不支持'); return; }
      const accepted = await promptCredentials(profile, fields, '', saveProfile);
      if (client !== rfb) return;
      if (!accepted) { await disconnect(); return; }
      updateCredentialFields(profile);
      const credentials = {};
      if (fields.includes('vncUsername')) credentials.username = profile.vncUsername;
      if (fields.includes('vncPassword')) credentials.password = profile.vncPassword;
      rfb.sendCredentials(credentials);
    });
    rfb.addEventListener('disconnect', (event) => {
      if (client !== rfb) return;
      disconnect(rfb.failureReason || (connected && event.detail.clean ? '远程桌面已关闭' : '无法继续连接远程桌面，请重试'));
    });
  } catch (err) {
    if (!disconnecting && connecting) await disconnect(errorMessage(err));
  }
}

async function disconnect(message = '') {
  if (disconnecting) return;
  disconnecting = true;
  connectionRevision++;
  connecting = false;
  connected = false;
  filesAvailable = false;
  transferring = false;
  showPanel(null);
  $('connection-metrics').hidden = true;
  resetPerformance();
  stopClipboard();
  pendingDrop = undefined;
  cancelCredentials();
  if ($('host-key-dialog').open) $('host-key-dialog').close();
  if ($('drop-dialog').open) $('drop-dialog').close();
  if ($('clipboard-dialog').open) $('clipboard-dialog').close();
  $('clipboard-text').value = '';
  const rfb = client;
  client = undefined;
  rfb?.disconnect();
  $('vnc-screen').hidden = true;
  $('connection-view').hidden = false;
  $('desktop-host').title = '';
  $('connection-endpoint').textContent = '尚未连接';
  $('connection-backend').textContent = '';
  status('未连接');
  showFilesOffline();
  showConnectionError(message);
  try {
    await call('Disconnect');
  } catch (err) {
    showConnectionError(errorMessage(err));
  } finally {
    $('vnc-screen').replaceChildren();
    disconnecting = false;
    syncControls();
  }
}

function formatSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 ** 2) return (bytes / 1024).toFixed(1) + ' KB';
  if (bytes < 1024 ** 3) return (bytes / 1024 ** 2).toFixed(1) + ' MB';
  return (bytes / 1024 ** 3).toFixed(1) + ' GB';
}

function join(directory, name) {
  return (directory === '/' ? '' : directory) + '/' + name;
}

function updateFileLocation() {
  $('file-location').hidden = !filesAvailable;
  $('file-location-path').textContent = fileLocationPath(fileLocation, directory) || '由服务端指定';
  $('file-location-note').textContent = fileLocationNote(fileLocation);
}

function renderBreadcrumbs() {
  const crumbs = $('breadcrumbs');
  crumbs.replaceChildren();
  let prefix = '';
  const names = directory.split('/').filter(Boolean);
  for (const [index, name] of [fileLocation?.home ? 'HOME' : '文件目录', ...names].entries()) {
    if (index > 0) {
      prefix += '/' + name;
      crumbs.append(' / ');
    }
    const target = prefix || '/';
    const crumb = document.createElement('button');
    crumb.type = 'button';
    crumb.textContent = name;
    crumb.title = target;
    if (index === names.length) crumb.setAttribute('aria-current', 'location');
    crumb.addEventListener('click', () => readDirectory(target));
    crumbs.append(crumb);
  }
  crumbs.scrollLeft = crumbs.scrollWidth;
}

function renderFiles() {
  list.replaceChildren();
  if (!entries.length) {
    listMessage('此目录为空');
    return;
  }
  for (const entry of entries) {
    const row = document.createElement('button');
    row.type = 'button';
    row.className = 'file-row';
    row.title = entry.name;
    if (!entry.dir) row.setAttribute('aria-pressed', String(entry === selection));
    const main = document.createElement('span');
    main.className = 'file-main';
    const icon = document.createElement('span');
    icon.className = 'file-icon' + (entry.dir ? ' folder' : '');
    icon.setAttribute('aria-hidden', 'true');
    const name = document.createElement('span');
    name.className = 'file-name';
    name.textContent = entry.name;
    main.append(icon, name);
    const size = document.createElement('span');
    size.className = 'file-size';
    size.textContent = entry.dir ? '—' : formatSize(entry.size);
    row.append(main, size);
    row.addEventListener('click', () => {
      if (entry.dir) {
        readDirectory(join(directory, entry.name));
        return;
      }
      list.querySelector('[aria-pressed="true"]')?.setAttribute('aria-pressed', 'false');
      row.setAttribute('aria-pressed', 'true');
      selection = entry;
      syncControls();
    });
    row.addEventListener('dblclick', () => {
      if (entry.regular && !transferring) download();
    });
    list.append(row);
  }
}

async function readDirectory(target) {
  if (!connected) return;
  const request = ++listRequest;
  selection = undefined;
  syncControls();
  listMessage('正在读取目录…');
  try {
    const next = await call('List', target);
    if (request !== listRequest) return;
    directory = target;
    updateFileLocation();
    entries = next;
    renderBreadcrumbs();
    renderFiles();
  } catch (err) {
    if (request === listRequest) {
      listMessage(errorMessage(err));
    }
  }
}

function transferMessage(text, error = false) {
  $('transfer-area').hidden = !text;
  $('transfer-area').classList.toggle('error', error);
  $('progress').hidden = true;
  $('transfer-message').textContent = text;
}

async function transfer(operation, target, refreshAfter) {
  if (!connected || !filesAvailable || transferring) return;
  const revision = connectionRevision;
  transferring = true;
  syncControls();
  transferMessage('准备传输…');
  try {
    const message = await operation();
    if (connectionRevision === revision) transferMessage(message);
  } catch (err) {
    if (connectionRevision === revision) transferMessage(errorMessage(err), true);
  } finally {
    if (connectionRevision === revision) {
      transferring = false;
      syncControls();
      if (refreshAfter && connected && directory === target) readDirectory(target);
    }
  }
}

function download() {
  if (!selection?.regular) return;
  const selected = selection;
  const target = directory;
  transfer(async () => {
    const complete = await call('Download', join(target, selected.name));
    return complete ? '已下载 ' + selected.name : '';
  }, target, false);
}

window.mygo.on('transfer-progress', ({ name, done, total }) => {
  if (!transferring || !connected) return;
  const progress = $('progress');
  progress.hidden = false;
  progress.max = Math.max(1, total);
  progress.value = done;
  $('transfer-message').textContent = name + ' · ' + formatSize(done) + ' / ' + formatSize(total);
});

window.mygo.on('mygo:file-drop', ({ paths, x, y }) => {
  filesPanel.classList.remove('dropping');
  if (!connected || !filesAvailable || transferring || !document.elementFromPoint(x, y)?.closest('#files-panel') ||
      !paths.length || $('drop-dialog').open) return;
  pendingDrop = { paths, target: directory };
  $('drop-destination').textContent = '目标目录 ' + (fileLocationPath(fileLocation, directory) || directory);
  $('drop-files').replaceChildren(...paths.map((file) => {
    const item = document.createElement('li');
    item.textContent = file.split(/[\\/]/).pop();
    return item;
  }));
  $('drop-dialog').returnValue = '';
  $('drop-dialog').showModal();
});

$('drop-dialog').addEventListener('close', () => {
  const drop = pendingDrop;
  pendingDrop = undefined;
  if (!drop || $('drop-dialog').returnValue !== 'upload') return;
  transfer(async () => {
    const count = await call('UploadDrop', drop.paths, drop.target);
    return '已上传 ' + count + ' 项';
  }, drop.target, true);
});

filesPanel.addEventListener('dragover', (event) => {
  if (!connected || !filesAvailable || transferring || !event.dataTransfer.types.includes('Files')) return;
  filesPanel.classList.add('dropping');
  clearTimeout(dragTimer);
  dragTimer = setTimeout(() => filesPanel.classList.remove('dropping'), 160);
});

form.addEventListener('submit', connect);
$('clipboard-toggle').addEventListener('click', () => {
  clipboard?.sync().catch(clipboardError);
  $('clipboard-dialog').showModal();
});
$('clipboard-send').addEventListener('click', () => {
  if (connected) clipboard?.send($('clipboard-text').value).catch(clipboardError);
});
$('clipboard-copy').addEventListener('click', () => {
  if (connected) clipboard?.receive($('clipboard-text').value).catch(clipboardError);
});
window.addEventListener('focus', () => clipboard?.sync().catch(clipboardError));
$('clipboard-copy').addEventListener('click', async () => {
  if (!connected) return;
  try {
    await clipboard?.receive($('clipboard-text').value);
  } catch (error) { clipboardError(error); }
});
window.addEventListener('focus', () => clipboard?.sync().catch(clipboardError));
$('clipboard-dialog').addEventListener('close', () => { if (connected) client?.focus(); });
$('quality-profile').addEventListener('change', selectQuality);
$('files-toggle').addEventListener('click', () => showPanel(activePanel === 'files' ? null : 'files'));
$('settings-toggle').addEventListener('click', () => showPanel(activePanel === 'settings' ? null : 'settings'));
$('send-secure-attention').addEventListener('click', () => {
  if (!connected || !client) return;
  client.sendCtrlAltDel();
  showPanel(null);
  client.focus();
});
$('files-close').addEventListener('click', () => showPanel(null));
$('settings-close').addEventListener('click', () => showPanel(null));
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && activePanel && !document.querySelector('dialog[open]')) {
    event.preventDefault();
    event.stopPropagation();
    showPanel(null);
  }
}, true);
$('disconnect').addEventListener('click', () => disconnect());
$('refresh').addEventListener('click', () => readDirectory(directory));
$('upload').addEventListener('click', () => {
  const target = directory;
  transfer(async () => {
    const count = await call('UploadDialog', target);
    return count ? '已上传 ' + count + ' 项' : '';
  }, target, true);
});
$('download').addEventListener('click', download);
$('mkdir').addEventListener('click', () => {
  $('folder-name').value = '';
  $('folder-dialog').returnValue = '';
  $('folder-dialog').showModal();
  $('folder-name').focus();
});
$('folder-dialog').addEventListener('close', async () => {
  if ($('folder-dialog').returnValue !== 'create' || !connected) return;
  const target = directory;
  try {
    await call('Mkdir', target, $('folder-name').value);
    if (directory === target) readDirectory(target);
  } catch (err) {
    transferMessage(errorMessage(err), true);
  }
});

async function init() {
  showFilesOffline();
  resetPerformance();
  try {
    const saved = localStorage.getItem('sshvnc.quality');
    if (Object.hasOwn(qualityProfiles, saved)) $('quality-profile').value = saved;
  } catch { /* Use balanced if preference storage is unavailable. */ }
  selectQuality();
  try {
    const server = await call('Defaults');
    const profile = server.profile;
    serverName = server.name;
    document.title = serverName + ' · JumpVNC';
    $('connection-name').textContent = serverName || profile.jump || profile.host || 'JumpVNC';
    setProfile(form, profile);
    profileReady = true;
    $('stored-row').hidden = !profile.storedPassword;
    $('state-endpoint').textContent = desktopEndpoint(profile);
    await connect();
  } catch (err) {
    status('无法读取连接');
    showConnectionError(errorMessage(err));
  }
}

function changeProtocol() {
  syncTransport(form);
  form.elements.storedPassword.checked = false;
  $('stored-row').hidden = true;
}
form.elements.protocol.addEventListener('change', changeProtocol);
form.elements.sshTunnel.addEventListener('change', changeProtocol);

$('reconnect').addEventListener('click', () => connect());
$('edit-connection').addEventListener('click', showConnectionForm);
$('connection-cancel').addEventListener('click', cancelConnectionForm);
$('connection-dialog').addEventListener('cancel', event => { event.preventDefault(); cancelConnectionForm(); });
$('save-connection').addEventListener('click', async () => {
  const button = $('save-connection');
  savingConnection = true;
  syncControls();
  button.disabled = true;
  $('connect').disabled = true;
  try {
    await saveProfile(connectOptions());
    editingSnapshot = undefined;
    $('connection-dialog').close();
    status('连接已保存');
    showConnectionError('');
  } catch (err) {
    showConnectionError(errorMessage(err));
  } finally {
    button.disabled = false;
    savingConnection = false;
    syncControls();
  }
});

init();
