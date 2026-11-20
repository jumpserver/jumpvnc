import RFB from '@novnc/novnc';

// UltraVNC 1.8.3.2: dmExtendOnly requests a display owned by this connection.
// This adapter uses noVNC 1.7.0 internals; recheck it when upgrading noVNC.
const EXTEND_ONLY = 3;
const EXT_VIEW_SIZE = -307; // rfbEncodingExtViewSize, 0xFFFFFECD

export default class UltraVNCRFB extends RFB {
  constructor(target, url, options, virtualSize) {
    super(target, url, options);
    this.virtualSize = virtualSize;
    this.virtualRequested = false;
    this.virtualViewport = undefined;
    if (virtualSize) {
      this.virtualTimer = setTimeout(() => {
        this.dispatchEvent(new CustomEvent('virtualdisplayerror', {
          detail: { message: '未能创建虚拟屏，请检查 UltraVNC 虚拟显示驱动和抓屏设置' },
        }));
      }, 20000);
      this.addEventListener('disconnect', () => clearTimeout(this.virtualTimer));
    }
  }

  _requestRemoteResize() {
    if (!this.virtualSize) return super._requestRemoteResize();
    if (!this._supportsSetDesktopSize || this._viewOnly || this.virtualRequested) return;
    this.virtualRequested = true;
    const { width, height } = this.virtualSize;
    RFB.messages.setDesktopSize(this._sock, width, height, 1, EXTEND_ONLY);
  }

  _handleRect() {
    if (this._FBU.encoding !== EXT_VIEW_SIZE) return super._handleRect();
    const { x, y, width, height } = this._FBU;
    // The driver can announce a topology change before its monitor is ready.
    // Keep the creation timeout running until a usable region arrives.
    if (this.virtualRequested && (!width || !height)) return true;
    if (!this.virtualRequested ||
        x + width > this._fbWidth || y + height > this._fbHeight) {
      return this._fail(`UltraVNC 虚拟屏范围无效：${x},${y} ${width}×${height}，画布 ${this._fbWidth}×${this._fbHeight}`);
    }
    this.virtualViewport = { x, y, width, height };
    clearTimeout(this.virtualTimer);
    this._updateClip();
    this._updateScale();
    this.dispatchEvent(new CustomEvent('virtualdisplay', { detail: this.virtualViewport }));
    return true;
  }

  _fail(details) {
    this.failureReason = details;
    return super._fail(details);
  }

  _updateClip() {
    if (!this.virtualViewport) return super._updateClip();
    const { x, y, width, height } = this.virtualViewport;
    this._display.clipViewport = true;
    this._display.viewportChangeSize(width, height);
    const current = this._display._viewportLoc;
    this._display.viewportChangePos(x - current.x, y - current.y);
    this._setClippingViewport(true);
    this._saveExpectedClientSize();
  }
}
