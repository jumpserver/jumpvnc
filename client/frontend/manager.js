import { selectServers } from './catalog.js';
import { syncTransport, setProfile, profileFromForm, desktopEndpoint } from './transport.js';

if (['darwin', 'win32'].includes(window.mygo?.platform)) {
  document.documentElement.classList.add('native-material');
}

const $ = id => document.getElementById(id);
const call = (method, ...args) => window.mygo.call('Manager.' + method, ...args);
const serverForm = $('server-form');
const folderForm = $('folder-form');
let library = { folders: [], servers: [] };
let defaults;
let view = 'all';
let folderID = '';
let editingServer;
let editingFolder;
let deletion;
let ready = false;
let reading = false;
let mutating = false;
let toastTimer;
let pendingImports = true; // Drain files delivered before this page subscribed.
const opening = new Set();

function icon(name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.classList.add('icon');
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS(svg.namespaceURI, 'use');
  use.setAttribute('href', '#i-' + name);
  svg.append(use);
  return svg;
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function errorText(error) { return error?.message || String(error); }
function errorAt(id, message = '') {
  $(id).textContent = message;
  $(id).hidden = !message;
}
function toast(message) {
  clearTimeout(toastTimer);
  $('toast').querySelector('span').textContent = message;
  $('toast').hidden = false;
  toastTimer = setTimeout(() => { $('toast').hidden = true; }, 2600);
}

function accept(next) {
  library = next;
  if (view === 'folder' && folderID && !library.folders.some(f => f.id === folderID)) {
    view = 'all';
    folderID = '';
  }
  render();
}

function navigate(next, id = '') {
  view = next;
  folderID = id;
  render();
  $('servers').scrollTop = 0;
  document.querySelector('.content').scrollTop = 0;
}

function renderNavigation() {
  $('all-count').textContent = library.servers.length;
  $('favorite-count').textContent = library.servers.filter(s => s.favorite).length;
  $('workspace-count').textContent = `${library.servers.length} 个连接 · ${library.folders.length} 个分类`;
  for (const button of document.querySelectorAll('[data-view]')) {
    const selected = button.dataset.view === view;
    button.classList.toggle('selected', selected);
    if (selected) button.setAttribute('aria-current', 'page');
    else button.removeAttribute('aria-current');
  }
  $('folders').replaceChildren();
  const uncategorized = { id: '', name: '未分类' };
  for (const folder of [...library.folders, uncategorized]) {
    const button = element('button', 'nav-item');
    button.type = 'button';
    button.title = folder.name;
    const selected = view === 'folder' && folderID === folder.id;
    button.classList.toggle('selected', selected);
    if (selected) button.setAttribute('aria-current', 'page');
    button.append(icon(folder.id ? 'folder' : 'box'), element('span', '', folder.name),
      element('span', 'nav-count', String(library.servers.filter(s => s.folderId === folder.id).length)));
    button.addEventListener('click', () => navigate('folder', folder.id));
    $('folders').append(button);
  }
}

function folderName(id) { return library.folders.find(f => f.id === id)?.name || '未分类'; }

function closeMenus(except) {
  for (const menu of document.querySelectorAll('.card-menu[open], #workspace-menu[open]')) {
    if (menu !== except) menu.open = false;
  }
}

function card(server) {
  const profile = server.profile;
  const article = element('article', 'server-card');
  // Stable icon colors help distinguish connections without implying availability.
  article.dataset.tone = String([...server.name].reduce((n, c) => n + c.codePointAt(0), 0) % 4);
  const open = element('button', 'server-open');
  open.type = 'button';
  open.title = `在新窗口中打开 ${server.name}\n${desktopEndpoint(profile)}${profile.jump ? '\n跳板机：' + profile.jump : ''}`;
  const tile = element('span', 'server-icon');
  tile.append(icon('monitor'));
  const info = element('span', 'server-info');
  info.append(element('strong', '', server.name), element('span', '', desktopEndpoint(profile)));
  open.append(tile, info);
  const footer = element('div', 'server-footer');
  const folder = element('span', 'folder-tag');
  folder.title = folderName(server.folderId);
  folder.append(icon('folder'), element('span', '', folderName(server.folderId)));
  const protocol = element('span', 'server-protocol', profile.transport === 'subsystem' ? 'SVNC' : profile.transport === 'vnc' ? 'VNC' : 'VNC · SSH 隧道');
  const actions = element('div', 'card-actions');
  const favorite = element('button', 'icon-button favorite');
  favorite.type = 'button';
  favorite.title = server.favorite ? '取消收藏' : '收藏连接';
  favorite.setAttribute('aria-label', `${favorite.title}：${server.name}`);
  favorite.setAttribute('aria-pressed', String(server.favorite));
  favorite.disabled = mutating;
  favorite.append(icon('star'));
  favorite.addEventListener('click', async () => {
    await mutate(() => call('Favorite', server.id, !server.favorite));
  });
  const menu = element('details', 'card-menu');
  const summary = element('summary', 'icon-button');
  summary.title = '更多操作';
  summary.setAttribute('aria-label', `${server.name}的更多操作`);
  summary.append(icon('more'));
  const items = element('div', 'menu-items');
  const edit = element('button', '', '编辑连接');
  edit.type = 'button';
  edit.addEventListener('click', () => { menu.open = false; showServer(server); });
  const exportFile = element('button', '', '导出 .svnc');
  exportFile.type = 'button';
  exportFile.addEventListener('click', async () => {
    menu.open = false;
    try {
      if (await call('ExportServer', server.id)) toast('连接已导出');
    } catch (error) { errorAt('error', errorText(error)); }
  });
  const remove = element('button', 'delete-action', '删除连接');
  remove.type = 'button';
  remove.addEventListener('click', () => {
    menu.open = false;
    confirmDelete('server', server.id, server.name);
  });
  items.append(edit, exportFile, remove);
  menu.append(summary, items);
  summary.addEventListener('click', () => closeMenus(menu));
  const connect = element('button', 'connect-button', opening.has(server.id) ? '打开中…' : '连接');
  connect.type = 'button';
  connect.disabled = opening.has(server.id);
  connect.setAttribute('aria-label', `连接 ${server.name}`);
  connect.append(icon('arrow'));
  actions.append(favorite, menu, connect);
  footer.append(folder, protocol, actions);
  article.append(open, footer);
  article.addEventListener('click', event => {
    if (event.target.closest('.favorite, .card-menu')) return;
    openServer(server);
  });
  return article;
}

function render() {
  renderNavigation();
  const name = view === 'all' ? '所有连接' : view === 'favorites' ? '我的收藏' : folderName(folderID);
  $('breadcrumb-name').textContent = name;
  $('breadcrumb-icon').querySelector('use').setAttribute('href', '#i-' + (view === 'all' ? 'grid' : view === 'favorites' ? 'star' : 'folder'));
  $('page-title').textContent = name;
  $('page-description').textContent = view === 'all' ? '集中管理你的远程桌面。'
    : view === 'favorites' ? '常用连接，随手可达。' : '按分类查找和管理你的连接。';
  $('folder-actions').hidden = view !== 'folder' || !folderID;
  const query = $('search').value;
  const servers = selectServers(library, view, folderID, query, $('sort').value);
  $('servers').replaceChildren(...servers.map(card));
  $('result-count').textContent = servers.length;
  $('status-count').textContent = query.trim() ? `找到 ${servers.length} 个连接 · 共 ${library.servers.length} 个`
    : `${servers.length} 个连接${view !== 'all' ? ' · 共 ' + library.servers.length + ' 个' : ''}`;
  $('empty').hidden = servers.length > 0 || !ready;
  $('empty-add').hidden = !!query.trim() || view === 'favorites';
  if (query.trim()) {
    $('empty-title').textContent = '没有找到匹配的连接';
    $('empty-description').textContent = '试试连接名称、主机地址，或其他关键词。';
  } else if (view === 'favorites') {
    $('empty-title').textContent = '还没有收藏的连接';
    $('empty-description').textContent = '点击连接卡片上的星标，将常用桌面收藏到这里。';
  } else {
    $('empty-title').textContent = view === 'folder' ? '这个分类还没有连接' : '添加你的第一个连接';
    $('empty-description').textContent = '保存常用的 VNC 服务器，随时在独立窗口中连接。';
  }
  syncControls();
}

function syncControls() {
  const blocked = !ready || mutating;
  for (const id of ['add-server', 'import-connections', 'add-folder', 'empty-add', 'rename-folder', 'delete-folder']) $(id).disabled = blocked;
  $('refresh').disabled = reading || mutating;
  $('workspace-refresh').disabled = reading || mutating;
  $('workspace-add-folder').disabled = blocked;
  for (const id of ['save-server', 'save-folder', 'confirm-delete']) $(id).disabled = mutating;
  for (const button of document.querySelectorAll('.dialog-close')) button.disabled = mutating;
  for (const button of document.querySelectorAll('.favorite, .menu-items button')) button.disabled = blocked;
}

async function refresh() {
  if (reading || mutating) return;
  reading = true;
  syncControls();
  try {
    const [next, profile] = await Promise.all([call('Library'), call('Defaults')]);
    defaults = profile;
    ready = true;
    errorAt('error');
    accept(next);
  } catch (error) {
    if (!ready) $('status-count').textContent = '连接配置读取失败';
    errorAt('error', errorText(error));
  } finally {
    reading = false;
    syncControls();
    drainImports();
  }
}

async function mutate(operation, errorID = 'error') {
  if (mutating || !ready) return false;
  mutating = true;
  syncControls();
  errorAt(errorID);
  try {
    accept(await operation());
    errorAt('error');
    return true;
  } catch (error) {
    errorAt(errorID, errorText(error));
    return false;
  } finally {
    mutating = false;
    syncControls();
    drainImports();
  }
}

function acceptImport(result) {
  if (!result.files) return;
  accept(result.library);
  if (result.added || result.skipped) {
    $('search').value = '';
    navigate('all');
    const message = `已导入 ${result.added} 个连接${result.skipped ? `，跳过 ${result.skipped} 个重复连接` : ''}`;
    toast(message);
    $('status-count').textContent = `${library.servers.length} 个连接 · ${message}`;
  }
  errorAt('error', result.errors.join('\n'));
}

async function importConnections(method) {
  mutating = true;
  syncControls();
  try {
    acceptImport(await call(method));
  } catch (error) {
    errorAt('error', errorText(error));
  } finally {
    mutating = false;
    syncControls();
    drainImports();
  }
}

function drainImports() {
  if (!pendingImports || !ready || reading || mutating) return;
  pendingImports = false;
  importConnections('ImportPending');
}

window.mygo.on('svnc-import-requested', () => {
  pendingImports = true;
  drainImports();
});

async function openServer(server) {
  if (opening.has(server.id)) return;
  closeMenus();
  opening.add(server.id);
  render();
  try {
    await call('OpenServer', server.id);
    const saved = library.servers.find(s => s.id === server.id);
    if (saved) saved.lastOpened = Date.now();
    errorAt('error');
  } catch (error) {
    errorAt('error', errorText(error));
  } finally {
    opening.delete(server.id);
    render();
  }
}

function fillFolders(selected) {
  const options = [{ id: '', name: '未分类' }, ...library.folders].map(folder => {
    const option = element('option', '', folder.name);
    option.value = folder.id;
    return option;
  });
  serverForm.elements.folderId.replaceChildren(...options);
  serverForm.elements.folderId.value = selected;
}

function syncStoredPassword() {
  syncTransport(serverForm);
  const keys = ['host', 'port', 'user', 'jump', 'identity', 'knownHosts', 'hostKeyAlias', 'vncPort', 'transport'];
  const saved = editingServer?.profile || defaults;
  const current = profileFromForm(serverForm);
  const matches = saved && keys.every(key =>
    String(current[key]).trim() === String(saved[key]));
  serverForm.elements.vncPassword.placeholder = matches && saved.storedPassword ? '已保存，留空保留原密码' : '可选，连接时填写';
  serverForm.elements.sshPassword.placeholder = matches && saved.storedSSHPassword ? '已保存，留空保留原密码' : '可选，也可使用本机身份';
}

function showServer(server) {
  if (!ready || mutating) return;
  closeMenus();
  editingServer = server;
  serverForm.reset();
  errorAt('server-error');
  const profile = server?.profile || defaults;
  setProfile(serverForm, profile);
  serverForm.elements.storedPassword.checked = true;
  serverForm.elements.storedSSHPassword.checked = true;
  serverForm.elements.name.value = server?.name || '';
  fillFolders(server ? server.folderId : view === 'folder' ? folderID : '');
  $('server-dialog-title').textContent = server ? '编辑连接' : '添加连接';
  syncStoredPassword();
  $('server-dialog').showModal();
  serverForm.elements.name.focus();
}

function showFolder(folder) {
  if (!ready || mutating) return;
  editingFolder = folder;
  folderForm.reset();
  errorAt('folder-error');
  $('folder-dialog-title').textContent = folder ? '重命名分类' : '新建分类';
  $('save-folder').textContent = folder ? '保存名称' : '创建分类';
  folderForm.elements.name.value = folder?.name || '';
  $('folder-dialog').showModal();
  folderForm.elements.name.focus();
}

function confirmDelete(kind, id, name) {
  if (mutating) return;
  deletion = { kind, id };
  errorAt('confirm-error');
  $('confirm-title').textContent = kind === 'server' ? '删除连接？' : '删除分类？';
  $('confirm-description').textContent = kind === 'server'
    ? `将删除“${name}”的本地连接配置，已打开的桌面窗口可继续使用。`
    : `将删除“${name}”，其中的连接会移到“未分类”。`;
  $('confirm-dialog').showModal();
  $('confirm-dialog').querySelector('.dialog-close').focus();
}

serverForm.addEventListener('submit', async event => {
  event.preventDefault();
  const name = serverForm.elements.name.value;
  const folderId = serverForm.elements.folderId.value;
  const profile = profileFromForm(serverForm);
  const server = { id: editingServer?.id || '', name, folderId, profile, favorite: editingServer?.favorite || false };
  if (await mutate(() => call('SaveServer', server), 'server-error')) {
    $('server-dialog').close();
    toast(editingServer ? '连接已更新' : '连接已添加');
  }
});
serverForm.addEventListener('input', syncStoredPassword);
$('server-dialog').addEventListener('close', () => {
  serverForm.elements.vncPassword.value = '';
  serverForm.elements.sshPassword.value = '';
});

folderForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (await mutate(() => call('SaveFolder', { id: editingFolder?.id || '', name: folderForm.elements.name.value }), 'folder-error')) {
    $('folder-dialog').close();
    toast(editingFolder ? '分类已重命名' : '分类已创建');
  }
});

$('confirm-form').addEventListener('submit', async event => {
  event.preventDefault();
  const target = deletion;
  if (!target) return;
  if (await mutate(() => call(target.kind === 'server' ? 'DeleteServer' : 'DeleteFolder', target.id), 'confirm-error')) {
    $('confirm-dialog').close();
    toast(target.kind === 'server' ? '连接已删除' : '分类已删除，连接已移到未分类');
  }
});

for (const button of document.querySelectorAll('.dialog-close')) {
  button.addEventListener('click', () => { if (!mutating) button.closest('dialog').close(); });
}
for (const dialog of document.querySelectorAll('dialog')) {
  dialog.addEventListener('cancel', event => { if (mutating) event.preventDefault(); });
}
for (const button of document.querySelectorAll('[data-view]')) button.addEventListener('click', () => navigate(button.dataset.view));
$('add-server').addEventListener('click', () => showServer());
$('import-connections').addEventListener('click', () => {
  if (ready && !reading && !mutating) importConnections('ImportDialog');
});
$('empty-add').addEventListener('click', () => showServer());
$('add-folder').addEventListener('click', () => showFolder());
$('rename-folder').addEventListener('click', () => showFolder(library.folders.find(f => f.id === folderID)));
$('delete-folder').addEventListener('click', () => confirmDelete('folder', folderID, folderName(folderID)));
$('refresh').addEventListener('click', refresh);
$('workspace-refresh').addEventListener('click', () => {
  closeMenus();
  refresh();
});
$('workspace-add-folder').addEventListener('click', () => {
  closeMenus();
  showFolder();
});
$('workspace-menu').querySelector('summary').addEventListener('click', () => closeMenus($('workspace-menu')));
$('search').addEventListener('input', render);
$('sort').addEventListener('change', () => { render(); savePreference('sort', $('sort').value); });

function setLayout(list) {
  $('servers').classList.toggle('list-view', list);
  $('layout').setAttribute('aria-pressed', String(list));
  $('layout').title = list ? '切换为卡片视图' : '切换为列表视图';
  $('layout').setAttribute('aria-label', $('layout').title);
  $('layout').querySelector('use').setAttribute('href', '#i-' + (list ? 'grid' : 'list'));
}
function savePreference(key, value) {
  try { localStorage.setItem('sshvnc.manager.' + key, value); } catch { /* Optional UI preferences. */ }
}

function setSidebarCollapsed(collapsed) {
  closeMenus();
  document.body.classList.toggle('sidebar-collapsed', collapsed);
  $('connection-sidebar').inert = collapsed;
  $('connection-sidebar').setAttribute('aria-hidden', String(collapsed));
  $('sidebar-toggle').setAttribute('aria-expanded', String(!collapsed));
  const label = collapsed ? '展开侧栏' : '收起侧栏';
  $('sidebar-toggle').title = label;
  $('sidebar-toggle').setAttribute('aria-label', label);
}
$('sidebar-toggle').addEventListener('click', () => {
  const collapsed = !document.body.classList.contains('sidebar-collapsed');
  setSidebarCollapsed(collapsed);
  savePreference('sidebarCollapsed', String(collapsed));
});
$('layout').addEventListener('click', () => {
  const list = !$('servers').classList.contains('list-view');
  setLayout(list);
  savePreference('layout', list ? 'list' : 'grid');
});

document.addEventListener('click', event => { if (!event.target.closest('.card-menu, #workspace-menu')) closeMenus(); });
document.addEventListener('keydown', event => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k' && !document.querySelector('dialog[open]')) {
    event.preventDefault();
    setSidebarCollapsed(false);
    savePreference('sidebarCollapsed', 'false');
    $('search').focus();
    $('search').select();
  }
  if (event.key === 'Escape') closeMenus();
});

try {
  const sort = localStorage.getItem('sshvnc.manager.sort');
  if (['name', 'recent', 'created'].includes(sort)) $('sort').value = sort;
  setLayout(localStorage.getItem('sshvnc.manager.layout') === 'list');
  setSidebarCollapsed(localStorage.getItem('sshvnc.manager.sidebarCollapsed') === 'true');
} catch { /* Defaults work without localStorage. */ }
if (window.mygo?.platform !== 'darwin') document.querySelector('.search kbd').textContent = 'Ctrl K';
syncControls();
refresh();
window.addEventListener('focus', () => { if (ready && !mutating) refresh(); });
