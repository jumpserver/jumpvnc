// SPDX-License-Identifier: GPL-2.0-or-later
// Synthetic motion only: never captures a desktop or injects input.
#include "h264_encoder.h"
#include <cstdio>
#include <chrono>
#include <algorithm>

int main(int argc, char** argv) {
    H264Runtime runtime;
    H264Encoder encoder;
    const unsigned width = 2560, height = 1440, frames = 120;
    if (argc != 2 || !runtime.ready() || !encoder.init(width, height)) {
        fprintf(stderr, "init failed 0x%08lx\n", static_cast<unsigned long>(encoder.error()));
        return 1;
    }
    FILE* file = fopen(argv[1], "wb");
    if (!file) return 2;
    std::vector<uint8_t> image(static_cast<size_t>(width) * height * 4);
    for (unsigned y = 0; y < height; ++y)
        for (unsigned x = 0; x < width; ++x) {
            auto p = image.data() + (static_cast<size_t>(y) * width + x) * 4;
            p[0] = x % 256; p[1] = y % 256; p[2] = (x ^ y) % 256; p[3] = 255;
        }
    uint64_t total = 0;
    unsigned outputFrames = 0;
    const auto started = std::chrono::steady_clock::now();
    for (unsigned i = 0; i < frames; ++i) {
        // A 960x540 window changes every frame; background remains static.
        for (unsigned y = 200; y < 740; ++y)
            for (unsigned x = 400; x < 1360; ++x) {
                auto p = image.data() + (static_cast<size_t>(y) * width + x) * 4;
                p[0] = (x + i * 3) % 256;
                p[1] = (y + i * 5) % 256;
                p[2] = ((x + i * 7) ^ (y + i * 3)) % 256;
            }
        std::vector<uint8_t> bytes;
        if (!encoder.encode(image.data(), width * 4, static_cast<int64_t>(i) * 10000000 / 30, bytes)) {
            fprintf(stderr, "encode frame=%u failed 0x%08lx\n", i, static_cast<unsigned long>(encoder.error()));
            fclose(file); return 3;
        }
        if (!bytes.empty()) ++outputFrames;
        total += bytes.size();
        if (fwrite(bytes.data(), 1, bytes.size(), file) != bytes.size()) {
            fclose(file); return 4;
        }
    }
    fclose(file);
    const double seconds = std::chrono::duration<double>(std::chrono::steady_clock::now() - started).count();
    printf("frames=%u outputs=%u bytes=%llu media_mbps=%.3f encode_fps=%.2f\n", frames,
           outputFrames, static_cast<unsigned long long>(total), total * 8.0 / (frames / 30.0) / 1e6,
           frames / seconds);
    return outputFrames == frames ? 0 : 5;
}
