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

// First trial uses the built-in synchronous Microsoft encoder. No downloaded
// codec DLLs, asynchronous hardware drivers, or worker queues are required.
class H264Encoder {
public:
    static constexpr unsigned bitrate = 6000000;
    static constexpr unsigned fps = 30;
    bool init(unsigned width, unsigned height);
    bool encode(const uint8_t* bgra, size_t pitch, int64_t time100ns,
                std::vector<uint8_t>& annexB);
    unsigned width() const { return width_; }
    unsigned height() const { return height_; }
    HRESULT error() const { return error_; }

private:
    Microsoft::WRL::ComPtr<IMFTransform> transform_;
    Microsoft::WRL::ComPtr<ICodecAPI> codec_;
    unsigned width_ = 0, height_ = 0;
    HRESULT error_ = S_OK;
    std::vector<uint8_t> nv12_;
    bool first_ = true;
    bool check(HRESULT result) { error_ = result; return SUCCEEDED(result); }
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
