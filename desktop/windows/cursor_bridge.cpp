// SPDX-License-Identifier: GPL-2.0-or-later
#define WIN32_LEAN_AND_MEAN
#include <winsock2.h>
#include "cursor_bridge.h"
#include <cstdint>
#include <cstring>
#include <set>
#include <vector>

// Owned by the desktop event loop, just like LibVNCServer's cursor updates.
static std::set<rfbClientPtr> alphaClients;
extern "C" void svncCursorReset(rfbClientPtr cl) { alphaClients.erase(cl); }
extern "C" void svncCursorEnable(rfbClientPtr cl) {
    alphaClients.insert(cl);
    cl->enableCursorShapeUpdates = TRUE;
    cl->cursorWasChanged = TRUE;
}

extern "C" rfbBool svncSendCursorShape(rfbClientPtr cl) {
    if (!alphaClients.count(cl)) return rfbSendCursorShape(cl);
    auto cursor = cl->screen->getCursorPtr(cl);
    unsigned w = cursor ? cursor->width : 0, h = cursor ? cursor->height : 0;
    if (cursor && w == 1 && h == 1 && cursor->mask && !cursor->mask[0]) w = h = 0;
    if (w > 256 || h > 256) return FALSE;
    if (w && h && !cursor->richSource) rfbMakeRichCursorFromXCursor(cl->screen, cursor);
    const auto& format = cl->screen->serverFormat;
    if (w && h && (!cursor->richSource || format.bitsPerPixel != 32)) return FALSE;
    std::vector<uint8_t> rgba(w * h * 4);
    for (unsigned y = 0; y < h; ++y) for (unsigned x = 0; x < w; ++x) {
        const auto i = y * w + x;
        uint32_t pixel;
        memcpy(&pixel, cursor->richSource + i * 4, 4);
        const unsigned alpha = cursor->alphaSource ? cursor->alphaSource[i]
            : (cursor->mask && (cursor->mask[y * ((w + 7) / 8) + x / 8] & (0x80 >> (x % 8))) ? 255 : 0);
        for (unsigned channel = 0; channel < 3; ++channel) {
            const unsigned shift = channel == 0 ? format.redShift : channel == 1 ? format.greenShift : format.blueShift;
            const unsigned maximum = channel == 0 ? format.redMax : channel == 1 ? format.greenMax : format.blueMax;
            unsigned color = maximum ? ((pixel >> shift) & maximum) * 255 / maximum : 0;
            if (cursor->alphaPreMultiplied && alpha) color = color * 255 / alpha;
            rgba[i * 4 + channel] = alpha ? static_cast<uint8_t>(color > 255 ? 255 : color) : 0;
        }
        rgba[i * 4 + 3] = static_cast<uint8_t>(alpha);
    }
    // VMware Cursor pseudo encoding: rectangle header, subtype 1, padding, RGBA.
    // noVNC and TigerVNC both support this straight-alpha wire representation.
    uint8_t header[14]{};
    auto word = [&](int offset, unsigned value) { header[offset] = value >> 8; header[offset + 1] = value; };
    word(0, w ? cursor->xhot : 0); word(2, h ? cursor->yhot : 0);
    word(4, w); word(6, h);
    header[8] = 0x57; header[9] = 0x4d; header[10] = 0x56; header[11] = 0x64;
    header[12] = 1;
    if (!rfbSendUpdateBuf(cl)) return FALSE;
    if (rfbWriteExact(cl, reinterpret_cast<char*>(header), sizeof(header)) < 0 ||
        (!rgba.empty() && rfbWriteExact(cl, reinterpret_cast<const char*>(rgba.data()), static_cast<int>(rgba.size())) < 0)) {
        rfbCloseClient(cl);
        return FALSE;
    }
    rfbStatRecordEncodingSent(cl, 0x574d5664, static_cast<int>(sizeof(header) + rgba.size()),
                              static_cast<int>(sizeof(header) + rgba.size()));
    return TRUE;
}
