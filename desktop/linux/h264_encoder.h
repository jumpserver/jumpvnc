// SPDX-License-Identifier: GPL-2.0-or-later
#pragma once
#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>
extern "C" {
#include <libavcodec/avcodec.h>
#include <libavutil/hwcontext.h>
#include <libswscale/swscale.h>
}

// All state belongs to the RFB event-loop thread. One submitted picture must
// produce one access unit immediately; encoders that buffer frames fall back.
class H264Encoder {
public:
    static constexpr unsigned bitrate = 6000000, fps = 30;
    H264Encoder() = default;
    H264Encoder(const H264Encoder&) = delete;
    H264Encoder& operator=(const H264Encoder&) = delete;
    ~H264Encoder();
    bool init(unsigned width, unsigned height, bool preferHardware = true);
    bool encode(const uint8_t* bgra, size_t pitch, int64_t timeMs, std::vector<uint8_t>& annexB);
    unsigned width() const { return width_; }
    unsigned height() const { return height_; }
    bool hardware() const { return hardware_; }
    const char* name() const { return name_.c_str(); }
    const char* error() const { return error_.c_str(); }
    uint64_t generation() const { return generation_; }
#ifdef SVNC_H264_TESTING
    void failHardwareForTest() { failHardware_ = true; }
#endif
private:
    AVCodecContext* codec_ = nullptr;
    AVFrame* frame_ = nullptr;
    AVFrame* hwFrame_ = nullptr;
    AVPacket* packet_ = nullptr;
    SwsContext* convert_ = nullptr;
    unsigned width_ = 0, height_ = 0;
    bool hardware_ = false, first_ = true;
    int64_t lastPts_ = -1;
    uint64_t generation_ = 0;
    std::string name_, error_;
#ifdef SVNC_H264_TESTING
    bool failHardware_ = false;
#endif
    void close();
    bool check(int result);
    bool open(const char* encoder, unsigned width, unsigned height, const char* device = nullptr);
    bool encodeFrame(const uint8_t* bgra, size_t pitch, int64_t timeMs, std::vector<uint8_t>& bytes);
};
