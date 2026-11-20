import test from 'node:test';
import assert from 'node:assert/strict';
import { selectServers } from './catalog.js';

const server = (id, name, folderId, favorite, createdAt, lastOpened, host) => ({
  id, name, folderId, favorite, createdAt, lastOpened,
  profile: { host, user: 'desktop', port: 5922, vncPort: 5900, jump: 'bastion' },
});
const library = {
  folders: [{ id: 'work', name: '工作环境' }, { id: 'lab', name: '测试环境' }],
  servers: [
    server('a', '工作站 10', 'work', true, 1, 30, '10.0.0.10'),
    server('b', '工作站 2', 'work', false, 3, 10, '10.0.0.2'),
    server('c', '测试机', 'lab', true, 2, 0, 'lab.example.com'),
    server('d', '个人电脑', '', false, 4, 20, 'home.example.com'),
  ],
};

test('search combines text terms with folder and favorite filters', () => {
  assert.deepEqual(selectServers(library, 'folder', 'work', '工作环境 10.0.0.2', 'name').map(s => s.id), ['b']);
  assert.deepEqual(selectServers(library, 'favorites', '', 'LAB.EXAMPLE', 'name').map(s => s.id), ['c']);
  assert.equal(selectServers(library, 'favorites', '', '工作站 2', 'name').length, 0);
  assert.deepEqual(selectServers(library, 'all', '', '未分类 bastion', 'name').map(s => s.id), ['d']);
  assert.deepEqual(selectServers(library, 'folder', '', '', 'name').map(s => s.id), ['d']);
});

test('natural name sorting, recent opens, and new connections do not mutate the library', () => {
  assert.deepEqual(selectServers(library, 'folder', 'work', '', 'name').map(s => s.id), ['b', 'a']);
  assert.deepEqual(selectServers(library, 'all', '', '', 'recent').map(s => s.id), ['a', 'd', 'b', 'c']);
  assert.deepEqual(selectServers(library, 'all', '', '', 'created').map(s => s.id), ['d', 'b', 'c', 'a']);
  assert.deepEqual(library.servers.map(s => s.id), ['a', 'b', 'c', 'd']);
});
