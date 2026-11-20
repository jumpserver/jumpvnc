import test from 'node:test';
import assert from 'node:assert/strict';
import { fileLocationPath, fileLocationNote } from './file-location.js';

test('show real Linux and Windows locations while keeping virtual SFTP paths', () => {
  assert.equal(fileLocationPath({ root: '/home/svnc' }), '/home/svnc');
  assert.equal(fileLocationPath({ root: '/home/svnc' }, '/Documents/中文'), '/home/svnc/Documents/中文');
  assert.equal(fileLocationPath({ root: 'C:\\Users\\Administrator' }, '/Desktop'), 'C:\\Users\\Administrator\\Desktop');
  assert.equal(fileLocationPath({ root: 'C:\\' }, '/Documents'), 'C:\\Documents');
  assert.equal(fileLocationPath({ root: '/' }, '/Documents'), '/Documents');
  assert.equal(fileLocationPath(undefined), '');
});

test('custom and older servers do not claim their directory is HOME', () => {
  assert.match(fileLocationNote({ root: '/home/svnc', home: true }), /HOME/);
  assert.doesNotMatch(fileLocationNote({ root: '/exchange', home: false }), /HOME/);
  assert.doesNotMatch(fileLocationNote(undefined), /HOME/);
});
