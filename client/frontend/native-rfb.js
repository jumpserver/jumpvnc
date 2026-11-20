// Compatibility adapter for the pinned noVNC 1.7.0 and native LibVNCServer backends.
export function nativeRFB(BaseRFB) {
  return class NativeRFB extends BaseRFB {
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
