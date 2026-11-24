// SPDX-License-Identifier: MPL-2.0
// Adapted from noVNC (Copyright (C) 2019 The noVNC authors).
// Encoding selection adapter for the pinned noVNC 1.7.0. Keep its capability
// checks and pseudo encodings; advertise H.264 only in the explicit video mode.
// The corresponding noVNC implementation is MPL-2.0 (bundled with the app).
export function videoRFB(BaseRFB, encodings, supportsH264) {
  return class VideoRFB extends BaseRFB {
    get preferVideo() { return this._preferVideo || false; }
    set preferVideo(value) {
      const changed = this.preferVideo !== Boolean(value);
      this._preferVideo = Boolean(value);
      if (changed && this._rfbConnectionState === 'connected') {
        this._sendEncodings();
        // Switching back to lossless-capable RFB must replace all video pixels,
        // including static areas that would not otherwise be marked dirty.
        BaseRFB.messages.fbUpdateRequest(this._sock, false, 0, 0, this._fbWidth, this._fbHeight);
      }
    }

    _sendEncodings() {
      const e = encodings;
      const list = [e.encodingCopyRect];
      if (this._fbDepth === 24) {
        if (this.preferVideo && supportsH264) list.push(e.encodingH264);
        list.push(e.encodingTight, e.encodingTightPNG, e.encodingZRLE,
          e.encodingJPEG, e.encodingHextile, e.encodingRRE, e.encodingZlib);
      }
      list.push(e.encodingRaw,
        e.pseudoEncodingQualityLevel0 + this._qualityLevel,
        e.pseudoEncodingCompressLevel0 + this._compressionLevel);
      for (const name of ['DesktopSize', 'LastRect', 'QEMUExtendedKeyEvent', 'QEMULedEvent',
        'ExtendedDesktopSize', 'Xvp', 'Fence', 'ContinuousUpdates', 'DesktopName',
        'ExtendedClipboard', 'ExtendedMouseButtons']) list.push(e['pseudoEncoding' + name]);
      if (this._fbDepth === 24) list.push(e.pseudoEncodingVMwareCursor, e.pseudoEncodingCursor);
      BaseRFB.messages.clientEncodings(this._sock, list);
    }
  };
}
