// SPDX-License-Identifier: GPL-2.0-or-later
// Synthetic pixels only; verify that every submitted picture decodes immediately.
#include "h264_encoder.h"
#include <chrono>
#include <cstdio>
#include <cstring>

static int fail(const char* message) { fprintf(stderr, "%s\n", message); return 1; }
int main(int argc, char** argv) {
    const char* mode = argc > 1 ? argv[1] : "auto";
    if (argc > 2 || (strcmp(mode, "auto") && strcmp(mode, "software") && strcmp(mode, "hardware-fallback")))
        return fail("usage: h264-smoke [auto|software|hardware-fallback]");
    H264Encoder encoder;
    if (encoder.init(0, 720) || encoder.init(1279, 720) || encoder.init(1280, 719) ||
        encoder.init(4098, 2160) || encoder.init(1280, 2162)) return fail("invalid geometry accepted");
    const AVCodec* implementation = avcodec_find_decoder(AV_CODEC_ID_H264);
    AVCodecContext* decoder = avcodec_alloc_context3(implementation);
    AVFrame* decoded = av_frame_alloc();
    AVPacket* packet = av_packet_alloc();
    if (!decoder || !decoded || !packet || avcodec_open2(decoder, implementation, nullptr) < 0)
        return fail("decoder initialization failed");
    uint64_t bytes = 0;
    unsigned count = 0;
    auto started = std::chrono::steady_clock::now();
    for (unsigned cycle = 0; cycle < 3; ++cycle) {
        // Exercise geometry resets and a fresh stream at an unchanged geometry.
        const unsigned width = cycle == 0 ? 1280 : 802, height = cycle == 0 ? 720 : 602;
        if (!encoder.init(width, height, strcmp(mode, "software"))) return fail(encoder.error());
        const bool inject = !strcmp(mode, "hardware-fallback");
        if (inject && !encoder.hardware()) return fail("hardware acceptance requires a real GPU encoder");
        const uint64_t generation = encoder.generation();
        std::vector<uint8_t> pixels(static_cast<size_t>(width) * height * 4);
        for (unsigned i = 0; i < 60; ++i) {
            for (unsigned y = 0; y < height; ++y) for (unsigned x = 0; x < width; ++x) {
                auto* pixel = pixels.data() + (static_cast<size_t>(y) * width + x) * 4;
                // Fixed red background, with a moving blue rectangle. The decoder
                // must preserve channel order/range as well as reference pictures.
                const bool moving = x > (i * 7) % (width - 200) && x < (i * 7) % (width - 200) + 200 && y < height / 2;
                pixel[0] = moving ? 255 : 0; pixel[1] = 0; pixel[2] = moving ? 0 : 255; pixel[3] = 255;
            }
            if (inject && i == 30) encoder.failHardwareForTest();
            std::vector<uint8_t> data;
            if (!encoder.encode(pixels.data(), width * 4, i * 1000 / H264Encoder::fps, data)) return fail(encoder.error());
            if (inject && i == 30 && (encoder.hardware() || encoder.generation() == generation))
                return fail("hardware failure did not reset to software");
            if (av_new_packet(packet, static_cast<int>(data.size())) < 0) return fail("packet allocation failed");
            memcpy(packet->data, data.data(), data.size());
            if (avcodec_send_packet(decoder, packet) < 0) return fail("invalid encoded access unit");
            av_packet_unref(packet);
            if (avcodec_receive_frame(decoder, decoded) < 0 || decoded->width != static_cast<int>(width) ||
                decoded->height != static_cast<int>(height)) return fail("missing/delayed/wrong-sized decoded frame");
            // BT.709 limited-range red is approximately Y=63 U=102 V=240.
            const unsigned x = width - 2, y = height - 2;
            if (decoded->format != AV_PIX_FMT_YUV420P ||
                decoded->data[0][y * decoded->linesize[0] + x] < 50 ||
                decoded->data[0][y * decoded->linesize[0] + x] > 76 ||
                decoded->data[1][(y / 2) * decoded->linesize[1] + x / 2] > 125 ||
                decoded->data[2][(y / 2) * decoded->linesize[2] + x / 2] < 210)
                return fail("decoded color/range mismatch");
            av_frame_unref(decoded);
            if (avcodec_receive_frame(decoder, decoded) != AVERROR(EAGAIN)) return fail("unexpected buffered picture");
            bytes += data.size(); ++count;
        }
        printf("cycle=%u encoder=%s hardware=%d generation=%llu size=%ux%u decoded=60\n",
               cycle, encoder.name(), int(encoder.hardware()), static_cast<unsigned long long>(encoder.generation()), width, height);
    }
    const auto ms = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - started).count();
    printf("frames=%u bytes=%llu nominal_mbps=%.3f encode_decode_ms=%.2f\n", count,
           static_cast<unsigned long long>(bytes), bytes * 8.0 * H264Encoder::fps / count / 1000000, ms * 1.0 / count);
    av_packet_free(&packet); av_frame_free(&decoded); avcodec_free_context(&decoder);
    return count == 180 ? 0 : fail("incomplete output");
}
