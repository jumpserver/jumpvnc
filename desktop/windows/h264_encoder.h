// SPDX-License-Identifier: GPL-2.0-or-later
#pragma once
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#include <mfidl.h>
#include <mftransform.h>
#include <strmif.h>
#include <codecapi.h>
#include <wrl/client.h>
#include <cstdint>
#include <vector>
#include <string>

// Prefer installed hardware MFTs; retain the built-in synchronous encoder as a
// fallback. One input picture is outstanding, so driver buffering stays bounded.
class H264Encoder {
public:
    H264Encoder() = default;
    H264Encoder(const H264Encoder&) = delete;
    H264Encoder& operator=(const H264Encoder&) = delete;
    static constexpr unsigned bitrate = 6000000;
    static constexpr unsigned fps = 30;
    ~H264Encoder();
    bool init(unsigned width, unsigned height, bool preferHardware = true);
    bool encode(const uint8_t* bgra, size_t pitch, int64_t time100ns,
                std::vector<uint8_t>& annexB);
    unsigned width() const { return width_; }
    unsigned height() const { return height_; }
    HRESULT error() const { return error_; }
    bool hardware() const { return hardware_; }
    const char* name() const { return name_.c_str(); }
    uint64_t generation() const { return generation_; }
#ifdef SVNC_H264_TESTING
    // Fault injection is compiled only into the synthetic acceptance binary.
    void failHardwareForTest() { failHardware_ = true; }
#endif

private:
    Microsoft::WRL::ComPtr<IMFTransform> transform_;
    Microsoft::WRL::ComPtr<ICodecAPI> codec_;
    Microsoft::WRL::ComPtr<IMFActivate> activation_;
    Microsoft::WRL::ComPtr<IMFMediaEventGenerator> events_;
    unsigned width_ = 0, height_ = 0;
    HRESULT error_ = S_OK;
    std::vector<uint8_t> nv12_;
    bool first_ = true;
    bool hardware_ = false;
    unsigned inputCredits_ = 0, outputCredits_ = 0;
    DWORD inputId_ = 0, outputId_ = 0;
    uint64_t generation_ = 0;
    std::string name_ = "Microsoft-software";
#ifdef SVNC_H264_TESTING
    bool failHardware_ = false;
#endif
    bool check(HRESULT result) { error_ = result; return SUCCEEDED(result); }
    void close();
    bool initHardware(unsigned width, unsigned height);
    bool initSoftware(unsigned width, unsigned height);
    bool configure(unsigned width, unsigned height);
    bool waitCredit(bool input, ULONGLONG deadline);
    bool submit(int64_t time100ns, std::vector<uint8_t>& bytes);
    bool readOutput(std::vector<uint8_t>& bytes);
    bool output(std::vector<uint8_t>& bytes);
};

// Must be constructed and destroyed on the encoding thread, outside encoders.
class H264Runtime {
public:
    H264Runtime();
    ~H264Runtime();
    bool ready() const { return started_; }
private:
    bool com_ = false, started_ = false;
};
