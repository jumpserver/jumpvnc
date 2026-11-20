import RFB from '@novnc/novnc';
import { applyQuality, NetworkWindow, qualityProfiles } from './performance.js';
import { measureRFB } from './rfb-performance.js';
import { nativeRFB } from './native-rfb.js';

const MeasuredRFB = measureRFB(nativeRFB(RFB));

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
let directory = '/';
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
  $('quality-settings').textContent = `请求画质 ${profile.quality}/9 · 压缩 ${profile.compression}/9`;
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
}

function showConnectionError(message) {
  $('connect-error').textContent = message;
  $('connect-error').hidden = !message;
}

function errorMessage(error) {
  return error?.message || String(error);
}

function syncControls() {
  $('connect').disabled = connecting || disconnecting;
  $('connect').textContent = connecting ? '连接中…' : '连接';
  $('disconnect').hidden = !connected && !connecting;
  $('disconnect').disabled = disconnecting;
  $('refresh').disabled = !connected || !filesAvailable;
  $('upload').disabled = !connected || !filesAvailable || transferring;
  $('download').disabled = !connected || !filesAvailable || transferring || !selection?.regular;
  $('mkdir').disabled = !connected || !filesAvailable || transferring;
  $('files-toggle').disabled = !connected || !filesAvailable;
  $('clipboard-toggle').disabled = !connected;
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
  listMessage('连接后显示远程文件');
  $('transfer-area').hidden = true;
  syncControls();
}

function connectOptions() {
  const p = Object.fromEntries(new FormData(form));
  return {
    ...p,
    host: p.host.trim(), user: p.user.trim(), jump: p.jump.trim(),
    port: Number(p.port), vncPort: Number(p.vncPort),
    storedPassword: form.elements.storedPassword.checked,
  };
}

async function connect(event) {
  event.preventDefault();
  if (connecting || connected || disconnecting) return;
  showConnectionError('');
  const profile = connectOptions();
  if (!profile.storedPassword && !profile.vncPassword) {
    showConnectionError('请输入 VNC 密码');
    form.elements.vncPassword.focus();
    return;
  }
  connecting = true;
  showPanel(null);
  $('connection-name').textContent = serverName || profile.jump || profile.host;
  syncControls();
  status('正在连接 SSH', 'connecting');
  try {
    const connection = await call('Connect', profile);
    if (disconnecting || !connecting) return;
    filesAvailable = connection.files;
    status('正在连接桌面', 'connecting');
    $('vnc-screen').hidden = false;
    const rfb = new MeasuredRFB($('vnc-screen'), connection.url, {
      shared: true,
      credentials: { password: connection.password },
    });
    client = rfb;
    let desktopName = '';
    const updateEndpoint = () => {
      if (client !== rfb) return;
      const endpoint = `${profile.user}@${profile.host} · SSH ${profile.port} → VNC ${profile.vncPort}`;
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
      if (client === rfb) $('clipboard-text').value = detail.text;
    });
    rfb.addEventListener('connect', () => {
      if (client !== rfb) return;
      connecting = false;
      connected = true;
      $('connection-metrics').hidden = false;
      updateEndpoint();
      $('connection-view').hidden = true;
      form.elements.vncPassword.value = '';
      status('已连接', 'connected');
      rfb.focus();
      syncControls();
      listMessage(filesAvailable ? '打开文件管理后显示交换目录' : '文件传输不可用，桌面仍可正常使用');
      startPerformance(rfb, connection.id);
    });
    rfb.addEventListener('securityfailure', () => {
      if (client === rfb) disconnect('VNC 认证失败，请检查 VNC 密码');
    });
    rfb.addEventListener('credentialsrequired', () => {
      if (client === rfb) disconnect('VNC 要求认证，请检查配置和 VNC 密码');
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
  pendingDrop = undefined;
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

function renderBreadcrumbs() {
  const crumbs = $('breadcrumbs');
  crumbs.replaceChildren();
  let prefix = '';
  const names = directory.split('/').filter(Boolean);
  for (const [index, name] of ['交换目录', ...names].entries()) {
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
  $('drop-destination').textContent = '目标目录 ' + directory;
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
$('clipboard-toggle').addEventListener('click', () => $('clipboard-dialog').showModal());
$('clipboard-send').addEventListener('click', () => {
  if (connected) client.clipboardPasteFrom($('clipboard-text').value);
});
$('quality-profile').addEventListener('change', selectQuality);
$('files-toggle').addEventListener('click', () => showPanel(activePanel === 'files' ? null : 'files'));
$('settings-toggle').addEventListener('click', () => showPanel(activePanel === 'settings' ? null : 'settings'));
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
    document.title = serverName + ' · SVNC';
    $('connection-name').textContent = serverName || profile.jump || profile.host || 'SVNC';
    for (const [key, value] of Object.entries(profile)) {
      if (key === 'storedPassword') {
        $('stored-row').hidden = !value;
        form.elements.storedPassword.checked = value;
      } else if (form.elements[key]) {
        form.elements[key].value = value;
      }
    }
    $('advanced').open = !profile.identity;
    if (profile.storedPassword) form.requestSubmit();
    else form.elements.vncPassword.focus();
  } catch (err) {
    showConnectionError(errorMessage(err));
  }
}

init();
