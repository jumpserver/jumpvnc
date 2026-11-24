import test from 'node:test';
import assert from 'node:assert/strict';
import { encodings } from './node_modules/@novnc/novnc/core/encodings.js';
import { videoRFB } from './video-rfb.js';
import { applyQuality } from './performance.js';

function fixture(support = true) {
  const sent = [];
  class Base {
    static messages = {
      clientEncodings(sock, list) { sent.push({ list }); },
      fbUpdateRequest(...args) { sent.push({ refresh: args }); },
    };
  }
  const client = new (videoRFB(Base, encodings, support))();
  Object.assign(client, { _fbDepth: 24, _qualityLevel: 6, _compressionLevel: 2,
    _fbWidth: 2560, _fbHeight: 1440, _sock: {}, _rfbConnectionState: 'connected' });
  return { client, sent };
}

test('normal RFB never advertises video; explicit video needs WebCodecs and full depth', () => {
  for (const support of [true, false]) {
    const { client, sent } = fixture(support);
    client._sendEncodings();
    assert(!sent.at(-1).list.includes(50));
    applyQuality(client, 'video');
    const list = sent.at(-2).list;
    assert.equal(list.includes(50), support);
    assert(list.includes(encodings.encodingTight));
    assert(list.every(Number.isInteger));
    assert(list.includes(encodings.pseudoEncodingExtendedClipboard));
    assert(list.includes(encodings.pseudoEncodingCursor));
    assert(list.includes(encodings.pseudoEncodingDesktopSize));
    client._fbDepth = 16;
    client._sendEncodings();
    assert(!sent.at(-1).list.includes(50));
  }
});

test('switching modes refreshes every pixel; repeated selection preserves encoder context', () => {
  const { client, sent } = fixture();
  applyQuality(client, 'video');
  assert.equal(sent.length, 2);
  applyQuality(client, 'video');
  assert.equal(sent.length, 2);
  applyQuality(client, 'balanced');
  assert.equal(sent.length, 4);
  assert(!sent.at(-2).list.includes(50));
  assert.deepEqual(sent.at(-1).refresh.slice(1), [false, 0, 0, 2560, 1440]);
});

test('selecting video before connecting affects the initial encoding negotiation', () => {
  const { client, sent } = fixture();
  client._rfbConnectionState = 'connecting';
  applyQuality(client, 'video');
  assert.equal(sent.length, 0);
  client._sendEncodings();
  assert(sent[0].list.includes(50));
});
