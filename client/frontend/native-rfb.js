// RFB cursor pixels use framebuffer coordinates, while browser cursors use CSS
// pixels. Keep both the shape and its hotspot aligned with the scaled desktop.
function scaleCursor(image, scale) {
  if (!image.w || !image.h || !Number.isFinite(scale) || scale <= 0 || scale === 1) return image;
  const w = Math.max(1, Math.round(image.w * scale));
  const h = Math.max(1, Math.round(image.h * scale));
  const rgbaPixels = new Uint8Array(w * h * 4);
  for (let y = 0; y < h; y++) {
    const sy = Math.min(image.h - 1, Math.floor((y + 0.5) * image.h / h));
    for (let x = 0; x < w; x++) {
      const sx = Math.min(image.w - 1, Math.floor((x + 0.5) * image.w / w));
      const source = (sy * image.w + sx) * 4;
      rgbaPixels.set(image.rgbaPixels.subarray(source, source + 4), (y * w + x) * 4);
    }
  }
  return { rgbaPixels, w, h,
    hotx: Math.min(w - 1, Math.round(image.hotx * w / image.w)),
    hoty: Math.min(h - 1, Math.round(image.hoty * h / image.h)) };
}

// Compatibility adapter for the pinned noVNC 1.7.0 and native LibVNCServer backends.
export function nativeRFB(BaseRFB) {
  return class NativeRFB extends BaseRFB {
    constructor(...args) {
      super(...args);
      // Native/synthetic WebView input can carry modifier flags without a
      // separate modifier keydown. Reconcile before noVNC handles the key.
      const modifiers = event => {
        const mappings = [
          ['shiftKey', 'ShiftLeft', 'ShiftRight', 0xffe1],
          ['ctrlKey', 'ControlLeft', 'ControlRight', 0xffe3],
          ['metaKey', 'MetaLeft', 'MetaRight', 0xffeb],
        ];
        // Release old modifiers first: Command and Control can map to the
        // same remote key when switching between successive shortcuts.
        for (const [flag, left, right] of mappings) {
          if (event.code === left || event.code === right) continue;
          const held = this._keyboard._keyDownList;
          if (!event[flag]) {
            for (const code of [left, right]) {
              if (code in held) this._keyboard._sendKeyEvent(held[code], code, false);
            }
          }
        }
        for (const [flag, left, right, sym] of mappings) {
          if (event.code === left || event.code === right) continue;
          const held = this._keyboard._keyDownList;
          if (event[flag] && !(left in held) && !(right in held)) {
            this._keyboard._sendKeyEvent(sym, left, true);
          }
        }
      };
      this._canvas?.addEventListener('keydown', modifiers, true);
      this._canvas?.addEventListener('keyup', modifiers, true);
    }

    _refreshCursor() {
      const original = this._cursorImage;
      // Keep noVNC's local dot fallback at its normal CSS size.
      if (!original || this._shouldShowDotCursor()) return super._refreshCursor();
      const scale = this._display?.scale ?? 1;
      if (this._nativeCursor?.image !== original || this._nativeCursor.scale !== scale) {
        this._nativeCursor = { image: original, scale, scaled: scaleCursor(original, scale) };
      }
      this._cursorImage = this._nativeCursor.scaled;
      try { return super._refreshCursor(); }
      finally { this._cursorImage = original; }
    }

    _updateScale() {
      super._updateScale();
      this._refreshCursor();
    }

    _handleKeyEvent(keysym, code, down, ...locks) {
      // A Mac's Command shortcuts should operate the Windows/Linux desktop.
      // Change the physical code too: QEMU key events use it, not the keysym.
      if (this.commandAsControl && (code === 'MetaLeft' || code === 'MetaRight')) {
        code = code === 'MetaLeft' ? 'ControlLeft' : 'ControlRight';
        keysym = code === 'ControlLeft' ? 0xffe3 : 0xffe4;
      }
      this._nativeControls ??= new Set();
      if (code === 'ControlLeft' || code === 'ControlRight') {
        if (down) this._nativeControls.add(code);
        else this._nativeControls.delete(code);
      }
      const paste = down && code === 'KeyV' && this._nativeControls.size && this.beforePaste;
      const forward = () => super._handleKeyEvent(keysym, code, down, ...locks);
      if (paste || this._nativeKeyQueue) {
        const next = (this._nativeKeyQueue || Promise.resolve()).then(async () => {
          if (paste) await this.beforePaste();
          forward();
        }).catch(error => this.onClipboardError?.(error));
        this._nativeKeyQueue = next;
        next.then(() => { if (this._nativeKeyQueue === next) this._nativeKeyQueue = undefined; });
        return;
      }
      return forward();
    }

    _handleServerCutText() {
      const previous = this._clipboardServerCapabilitiesFormats;
      const result = super._handleServerCutText();
      if (previous !== this._clipboardServerCapabilitiesFormats) {
        // Resend after negotiation; the first sync may have used Latin-1.
        this.dispatchEvent(new Event('clipboardcapabilities'));
      }
      return result;
    }

    _handleMouse(event) {
      // Some native WebView event sources report buttons=0 even on mousedown.
      // The changed button still identifies the press; preserve normal browser
      // masks (including multiple held buttons) whenever they are available.
      if (event.type === 'mousedown' && event.buttons === 0) {
        const pressed = [1, 4, 2, 8, 16][event.button];
        if (pressed) Object.defineProperty(event, 'buttons', { value: pressed, configurable: true });
      }
      return super._handleMouse(event);
    }

    clipboardPasteFrom(text) {
      const TEXT = 1, NOTIFY = 1 << 27, PROVIDE = 1 << 28;
      // LibVNCServer 0.9.15 accepts unsolicited UTF-8 Provide but does not
      // advertise Notify. noVNC otherwise falls back to lossy Latin-1.
      if (this._rfbConnectionState === 'connected' && !this._viewOnly &&
          this._clipboardServerCapabilitiesFormats[TEXT] &&
          this._clipboardServerCapabilitiesActions[PROVIDE] &&
          !this._clipboardServerCapabilitiesActions[NOTIFY]) {
        BaseRFB.messages.extendedClipboardProvide(this._sock, [TEXT], [text]);
        return;
      }
      return super.clipboardPasteFrom(text);
    }
  };
}
