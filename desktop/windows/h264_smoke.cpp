// SPDX-License-Identifier: GPL-2.0-or-later
// Synthetic motion only: never captures a desktop or injects input.
#include "h264_encoder.h"
#include <cstdio>
#include <chrono>
#include <algorithm>
#include <cstring>

static uint64_t processTime() {
    FILETIME created{}, exited{}, kernel{}, user{};
    if (!GetProcessTimes(GetCurrentProcess(), &created, &exited, &kernel, &user)) return 0;
    auto value = [](FILETIME t) { return (uint64_t(t.dwHighDateTime) << 32) | t.dwLowDateTime; };
    return value(kernel) + value(user);
}

static bool hasResetPicture(const std::vector<uint8_t>& data) {
    bool sps = false, pps = false, idr = false;
    for (size_t i = 0; i + 3 < data.size(); ++i) {
        if (data[i] || data[i + 1] || data[i + 2] != 1) continue;
        switch (data[i + 3] & 31) {
        case 7: sps = true; break;
        case 8: pps = true; break;
        case 5: idr = true; break;
        }
    }
    return sps && pps && idr;
}

int main(int argc, char** argv) {
    H264Runtime runtime;
    H264Encoder encoder;
    const unsigned width = 2560, height = 1440, frames = 120;
    const char* mode = argc == 3 ? argv[2] : "auto";
    const bool software = !strcmp(mode, "software"), fallback = !strcmp(mode, "fallback");
    if (argc < 2 || argc > 3 ||
        (strcmp(mode, "auto") && strcmp(mode, "hardware") && !software && !fallback) ||
        !runtime.ready() || !encoder.init(width, height, !software)) {
        fprintf(stderr, "init failed 0x%08lx\n", static_cast<unsigned long>(encoder.error()));
        return 1;
    }
    if ((!strcmp(mode, "hardware") || fallback) && !encoder.hardware()) {
        fprintf(stderr, "hardware acceptance requires an active hardware MFT\n"); return 6;
    }
    printf("encoder=%s hardware=%d generation=%llu\n", encoder.name(), int(encoder.hardware()),
           static_cast<unsigned long long>(encoder.generation()));
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
    const uint64_t cpuStarted = processTime();
    const uint64_t initialGeneration = encoder.generation();
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
#ifdef SVNC_H264_TESTING
        if (fallback && i == 60) encoder.failHardwareForTest();
#endif
        if (!encoder.encode(image.data(), width * 4, static_cast<int64_t>(i) * 10000000 / 30, bytes)) {
            fprintf(stderr, "encode frame=%u failed 0x%08lx\n", i, static_cast<unsigned long>(encoder.error()));
            fclose(file); return 3;
        }
        if (!bytes.empty()) ++outputFrames;
        if ((i == 0 || (fallback && i == 60)) && !hasResetPicture(bytes)) {
            fprintf(stderr, "new encoder stream lacks SPS/PPS/IDR\n");
            fclose(file); return 9;
        }
        if (!strcmp(mode, "hardware") && !encoder.hardware()) { fclose(file); return 7; }
        if (fallback && i >= 60 && (encoder.hardware() || encoder.generation() != initialGeneration + 1)) {
            fclose(file); return 8;
        }
        total += bytes.size();
        if (fwrite(bytes.data(), 1, bytes.size(), file) != bytes.size()) {
            fclose(file); return 4;
        }
    }
    fclose(file);
    const double seconds = std::chrono::duration<double>(std::chrono::steady_clock::now() - started).count();
    const double cpuMs = (processTime() - cpuStarted) / 10000.0;
    printf("frames=%u outputs=%u bytes=%llu media_mbps=%.3f encode_fps=%.2f cpu_ms_per_frame=%.3f final_hardware=%d generation=%llu\n", frames,
           outputFrames, static_cast<unsigned long long>(total), total * 8.0 / (frames / 30.0) / 1e6,
           frames / seconds, cpuMs / frames, int(encoder.hardware()),
           static_cast<unsigned long long>(encoder.generation()));
    return outputFrames == frames ? 0 : 5;
}
