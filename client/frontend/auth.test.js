import assert from 'node:assert/strict';
import { missingCredentials, applyCredentials } from './auth.js';

const base = { transport: 'subsystem', user: '', vncPassword: '', storedPassword: false };
assert.deepEqual(missingCredentials(base), ['user', 'vncPassword']);
assert.deepEqual(missingCredentials({ ...base, user: 'desktop' }), ['vncPassword']);
assert.deepEqual(missingCredentials({ ...base, storedPassword: true }), ['user']);
assert.deepEqual(missingCredentials({ ...base, transport: 'vnc' }), ['vncPassword']);
assert.deepEqual(missingCredentials({ ...base, user: 'desktop', storedPassword: true }), []);
const supplied = applyCredentials({ ...base, storedPassword: true, storedSSHPassword: true }, { user: 'entered', vncPassword: 'one-use', sshPassword: 'ssh-one-use' });
assert.equal(supplied.storedPassword, false);
assert.equal(supplied.storedSSHPassword, false);
assert.deepEqual(missingCredentials(supplied), []);

const { profileToSave } = await import('./auth.js');
const saved = profileToSave({ ...base, storedPassword: true }, { user: ' entered ', sshPassword: 'new-ssh' });
assert.equal(saved.user, 'entered');
assert.equal(saved.storedPassword, true);
assert.equal(saved.storedSSHPassword, true);
assert.equal(profileToSave(base, { vncPassword: 'new-vnc' }).storedPassword, true);
