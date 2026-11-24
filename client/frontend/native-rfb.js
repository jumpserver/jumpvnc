import { scaleCursor, densityCursor } from './cursor-image.js';

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
        if (this.commandAsControl) mappings.push(['altKey', 'AltLeft', 'AltRight', 0xffe9]);
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
      const density = globalThis.devicePixelRatio || 1;
      if (this._nativeCursor?.image !== original || this._nativeCursor.scale !== scale || this._nativeCursor.density !== density) {
        this._nativeCursor = { image: original, scale, density, scaled: scaleCursor(original, scale) };
      }
      this._cursorImage = this._nativeCursor.scaled;
      try {
        const result = super._refreshCursor();
        const target = this._cursor?._target;
        // noVNC's native cursor path uses a PNG URL. Keep touch/fallback
        // overlays untouched; image-set gives native Retina cursors real pixels
        // without doubling their CSS size or their click hotspot.
        if (density > 1 && original.w && original.h && target?.style.cursor.startsWith('url(') && globalThis.CSS?.supports) {
          if (this._nativeCursor.css === undefined) {
            const high = scaleCursor(original, scale * density);
            this._nativeCursor.css = densityCursor(high, density, this._nativeCursor.scaled, document, CSS.supports.bind(CSS));
          }
          if (this._nativeCursor.css) target.style.cursor = this._nativeCursor.css;
        }
        return result;
      }
      finally { this._cursorImage = original; }
    }

    _updateScale() {
      super._updateScale();
      this._refreshCursor();
    }

    _handleVMwareCursor() {
      // Both the subtype and its padding must arrive before noVNC consumes
      // either byte, including when SSH/WebSocket splits a cursor update.
      if (this._sock.rQwait('VMware cursor header', 2)) return false;
      return super._handleVMwareCursor();
    }

    _handleKeyEvent(keysym, code, down, ...locks) {
      // A Mac's Command shortcuts should operate the Windows/Linux desktop.
      // Change the physical code too: QEMU key events use it, not the keysym.
      if (this.commandAsControl && (code === 'MetaLeft' || code === 'MetaRight')) {
        code = code === 'MetaLeft' ? 'ControlLeft' : 'ControlRight';
        keysym = code === 'ControlLeft' ? 0xffe3 : 0xffe4;
      }
      // noVNC maps Mac Option to Mode_switch/ISO_Level3_Shift. Native Windows
      // desktops need actual Alt events for shortcuts and modifier release.
      if (this.commandAsControl && (code === 'AltLeft' || code === 'AltRight')) {
        keysym = code === 'AltLeft' ? 0xffe9 : 0xffea;
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
