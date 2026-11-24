// Cursor images contain straight RGBA. Filter premultiplied colors and alpha
// together so transparent pixels cannot introduce dark fringes at the edge.
export function scaleCursor(image, scale) {
  if (!image.w || !image.h || !Number.isFinite(scale) || scale <= 0 || scale === 1) return image;
  const w = Math.max(1, Math.round(image.w * scale));
  const h = Math.max(1, Math.round(image.h * scale));
  function samples(position, size, original) {
    const ratio = original / size;
    if (ratio < 1) {
      const center = (position + 0.5) * ratio - 0.5;
      const first = Math.floor(center), fraction = center - first;
      return [[Math.max(0, first), 1 - fraction], [Math.min(original - 1, first + 1), fraction]];
    }
    const start = position * ratio, end = (position + 1) * ratio;
    const result = [];
    for (let i = Math.floor(start); i < Math.min(original, Math.ceil(end)); i++) {
      result.push([i, (Math.min(end, i + 1) - Math.max(start, i)) / ratio]);
    }
    return result;
  }
  const xs = Array.from({ length: w }, (_, x) => samples(x, w, image.w));
  const ys = Array.from({ length: h }, (_, y) => samples(y, h, image.h));
  const rgbaPixels = new Uint8Array(w * h * 4);
  for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
    let alpha = 0;
    const rgb = [0, 0, 0];
    for (const [sy, wy] of ys[y]) for (const [sx, wx] of xs[x]) {
      const offset = (sy * image.w + sx) * 4;
      const coverage = image.rgbaPixels[offset + 3] * wx * wy;
      alpha += coverage;
      for (let c = 0; c < 3; c++) rgb[c] += image.rgbaPixels[offset + c] * coverage;
    }
    const offset = (y * w + x) * 4;
    for (let c = 0; c < 3; c++) rgbaPixels[offset + c] = alpha ? Math.round(rgb[c] / alpha) : 0;
    rgbaPixels[offset + 3] = Math.round(alpha);
  }
  return { rgbaPixels, w, h,
    hotx: Math.min(w - 1, Math.round(image.hotx * w / image.w)),
    hoty: Math.min(h - 1, Math.round(image.hoty * h / image.h)) };
}

export function densityCursor(image, density, hotspot, document, supports) {
  const canvas = document.createElement('canvas');
  canvas.width = image.w;
  canvas.height = image.h;
  const context = canvas.getContext('2d');
  const pixels = context.createImageData(image.w, image.h);
  pixels.data.set(image.rgbaPixels);
  context.putImageData(pixels, 0, 0);
  const url = canvas.toDataURL();
  const position = `${hotspot.hotx} ${hotspot.hoty}`;
  for (const name of ['image-set', '-webkit-image-set']) {
    const value = `${name}(url("${url}") ${density}x) ${position}, default`;
    if (supports('cursor', value)) return value;
  }
  return null;
}
