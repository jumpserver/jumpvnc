// SPDX-License-Identifier: GPL-2.0-or-later
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <winsock2.h>
#include "h264_bridge.h"
#include "h264_encoder.h"
#include <cstdio>
#include <map>
#include <memory>

struct VideoClient {
    H264Encoder encoder;
    bool reset = true;
    ULONGLONG lastFrame = 0;
    ULONGLONG reportAt = GetTickCount64();
    uint64_t bytes = 0, frames = 0, encodeMs = 0;
    int fallback = rfbEncodingRaw;
};
// LibVNCServer and these encoders are owned by the backend event-loop thread.
static std::map<rfbClientPtr, std::unique_ptr<VideoClient>> clients;

extern "C" rfbBool svncH264Select(rfbClientPtr cl) {
    static H264Runtime runtime;
    if (!runtime.ready() || clients.size() >= 2 || cl->screen != cl->scaledScreen || cl->format.bitsPerPixel != 32 ||
        !cl->format.trueColour) return FALSE;
    auto video = std::make_unique<VideoClient>();
    if (!video->encoder.init(cl->screen->width, cl->screen->height)) {
        fprintf(stderr, "h264.unavailable hr=0x%08lx size=%dx%d; retaining RFB fallback\n",
                static_cast<unsigned long>(video->encoder.error()), cl->screen->width, cl->screen->height);
        return FALSE;
    }
    clients[cl] = std::move(video);
    fprintf(stderr, "h264.ready encoder=Microsoft-software target_bps=%u max_fps=%u\n",
            H264Encoder::bitrate, H264Encoder::fps);
    return TRUE;
}
extern "C" void svncH264Reset(rfbClientPtr cl) { clients.erase(cl); }
extern "C" void svncH264Offered(rfbClientPtr cl, unsigned encoding) {
    auto found = clients.find(cl);
    if (found != clients.end() && encoding == rfbEncodingTight)
        found->second->fallback = rfbEncodingTight;
}

extern "C" rfbBool svncH264Send(rfbClientPtr cl) {
    auto found = clients.find(cl);
    if (found == clients.end()) return FALSE;
    auto& video = *found->second;
    bool ok = cl->screen == cl->scaledScreen;
    if (ok && (video.encoder.width() != static_cast<unsigned>(cl->screen->width) ||
               video.encoder.height() != static_cast<unsigned>(cl->screen->height))) {
        ok = video.encoder.init(cl->screen->width, cl->screen->height);
        video.reset = true;
    }
    // Bound cadence without creating a queue of historical input frames.
    // The next capture acquires the latest available desktop image.
    ULONGLONG now = GetTickCount64();
    if (video.lastFrame && now - video.lastFrame < 1000 / H264Encoder::fps)
        Sleep(static_cast<DWORD>(1000 / H264Encoder::fps - (now - video.lastFrame)));
    now = GetTickCount64();
    video.lastFrame = now;
    std::vector<uint8_t> bytes;
    ok = ok && video.encoder.encode(reinterpret_cast<const uint8_t*>(cl->screen->frameBuffer),
                                     cl->screen->paddedWidthInBytes, now * 10000, bytes);
    if (!ok) {
        const int fallback = video.fallback;
        fprintf(stderr, "h264.failed hr=0x%08lx; switching to encoding=%d\n",
                static_cast<unsigned long>(video.encoder.error()), fallback);
        clients.erase(found);
        // This update already announced one full-screen rectangle. Raw has
        // exactly one rectangle; Tight can split it, so switch on the next update.
        cl->preferredEncoding = fallback;
        return rfbSendRectEncodingRaw(cl, 0, 0, cl->scaledScreen->width, cl->scaledScreen->height);
    }
    video.encodeMs += GetTickCount64() - now;
    video.bytes += bytes.size();
    ++video.frames;
    if (now - video.reportAt >= 5000) {
        fprintf(stderr, "h264.stats fps=%.1f mbps=%.3f encode_ms=%.1f\n",
                video.frames * 1000.0 / (now - video.reportAt),
                video.bytes * 8.0 / (now - video.reportAt) / 1000,
                video.encodeMs * 1.0 / video.frames);
        video.frames = video.bytes = video.encodeMs = 0;
        video.reportAt = now;
    }
    // Flush any cursor/resize metadata before writing the video rectangle.
    if (!rfbSendUpdateBuf(cl)) return FALSE;
    uint8_t header[20]{};
    auto word = [&](int offset, unsigned value) {
        header[offset] = static_cast<uint8_t>(value >> 8);
        header[offset + 1] = static_cast<uint8_t>(value);
    };
    auto dword = [&](int offset, unsigned value) {
        word(offset, value >> 16); word(offset + 2, value);
    };
    word(4, cl->screen->width); word(6, cl->screen->height);
    dword(8, 50); dword(12, static_cast<unsigned>(bytes.size()));
    dword(16, video.reset ? 2 : 0);
    video.reset = false;
    if (rfbWriteExact(cl, reinterpret_cast<char*>(header), sizeof(header)) < 0 ||
        (!bytes.empty() && rfbWriteExact(cl, reinterpret_cast<const char*>(bytes.data()),
                                        static_cast<int>(bytes.size())) < 0)) {
        rfbCloseClient(cl);
        return FALSE;
    }
    rfbStatRecordEncodingSent(cl, 50, static_cast<int>(sizeof(header) + bytes.size()),
                              cl->screen->width * cl->screen->height * 4);
    return TRUE;
}
