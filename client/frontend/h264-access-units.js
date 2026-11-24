// SPDX-License-Identifier: MPL-2.0
// Adapter for noVNC (Copyright (C) 2024 The noVNC authors).
// noVNC 1.7.0 stops each H.264 chunk at the first VCL NAL. That works for
// single-slice encoders, but Windows MF emits several slices per picture.
// WebCodecs needs a complete access unit, otherwise noVNC waits for a decoded
// output for every slice even though the decoder produces only one picture.
export function installH264AccessUnits(Parser) {
  Parser.prototype.parse = function () {
    const start = this._index;
    let picture = false, key = false;
    while (this._index < this._data.length) {
      const prefix = this._getStartSequenceLen(this._index);
      if (!prefix) throw new Error('Invalid H.264 Annex B start sequence');
      const nal = this._index + prefix;
      if (nal >= this._data.length || (this._data[nal] & 0x80))
        throw new Error('Invalid H.264 NAL header');
      const type = this._data[nal] & 31;
      const slice = type === 1 || type === 5;
      // first_mb_in_slice is unsigned Exp-Golomb: zero starts with a 1 bit.
      const firstSlice = slice && nal + 1 < this._data.length && (this._data[nal + 1] & 0x80);
      // AUD and leading parameter/SEI NALs belong to the following picture.
      if (picture && (firstSlice || [6, 7, 8, 9].includes(type))) break;
      if (type === 7) {
        if (nal + 3 >= this._data.length) throw new Error('Truncated H.264 SPS');
        this._parseSps(nal + 1);
      }
      picture ||= slice;
      key ||= type === 5;
      const next = this._indexOfNextNalUnit(nal + 1);
      this._index = next < 0 ? this._data.length : next;
    }
    return picture ? { frame: this._data.subarray(start, this._index), key } : null;
  };
}
