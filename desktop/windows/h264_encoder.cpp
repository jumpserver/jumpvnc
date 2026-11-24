// SPDX-License-Identifier: GPL-2.0-or-later
#include "h264_encoder.h"
#include <mfapi.h>
#include <mferror.h>
#include <wmcodecdsp.h>
#include <algorithm>
#include <cstring>

using Microsoft::WRL::ComPtr;

H264Runtime::H264Runtime() {
    const HRESULT hr = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    com_ = SUCCEEDED(hr);
    if (com_ || hr == RPC_E_CHANGED_MODE)
        started_ = SUCCEEDED(MFStartup(MF_VERSION, MFSTARTUP_NOSOCKET));
}
H264Runtime::~H264Runtime() {
    if (started_) MFShutdown();
    if (com_) CoUninitialize();
}

static bool videoType(IMFMediaType* type, const GUID& subtype, unsigned w, unsigned h) {
    return SUCCEEDED(type->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video)) &&
           SUCCEEDED(type->SetGUID(MF_MT_SUBTYPE, subtype)) &&
           SUCCEEDED(type->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive)) &&
           SUCCEEDED(MFSetAttributeSize(type, MF_MT_FRAME_SIZE, w, h)) &&
           SUCCEEDED(MFSetAttributeRatio(type, MF_MT_FRAME_RATE, H264Encoder::fps, 1)) &&
           SUCCEEDED(MFSetAttributeRatio(type, MF_MT_PIXEL_ASPECT_RATIO, 1, 1));
}

bool H264Encoder::init(unsigned width, unsigned height) {
    if (!width || !height || width > 4096 || height > 2160 || (width & 1) || (height & 1))
        return check(E_INVALIDARG);
    transform_.Reset();
    codec_.Reset();
    if (!check(CoCreateInstance(CLSID_CMSH264EncoderMFT, nullptr, CLSCTX_INPROC_SERVER,
                               IID_PPV_ARGS(&transform_))) ||
        !check(transform_->QueryInterface(IID_ICodecAPI, reinterpret_cast<void**>(codec_.GetAddressOf()))))
        return false;
    auto setting = [&](const GUID& key, ULONG value) {
        VARIANT v{}; v.vt = VT_UI4; v.ulVal = value;
        return codec_->SetValue(&key, &v);
    };
    // CBR is a long-term target, not a hard cap on individual key frames.
    if (!check(setting(CODECAPI_AVEncCommonRateControlMode, eAVEncCommonRateControlMode_CBR)) ||
        !check(setting(CODECAPI_AVEncCommonMeanBitRate, bitrate)) ||
        !check(setting(CODECAPI_AVEncMPVDefaultBPictureCount, 0)))
        return false;
    VARIANT lowLatency{}; lowLatency.vt = VT_BOOL; lowLatency.boolVal = VARIANT_TRUE;
    if (!check(codec_->SetValue(&CODECAPI_AVLowLatencyMode, &lowLatency)))
        return false;
    setting(CODECAPI_AVEncMPVGOPSize, fps * 2);
    setting(CODECAPI_AVEncCommonQualityVsSpeed, 20);
    ComPtr<IMFMediaType> out, in;
    if (!check(MFCreateMediaType(&out)) || !check(MFCreateMediaType(&in)) ||
        !videoType(out.Get(), MFVideoFormat_H264, width, height) ||
        !videoType(in.Get(), MFVideoFormat_NV12, width, height) ||
        !check(out->SetUINT32(MF_MT_AVG_BITRATE, bitrate)) ||
        !check(out->SetUINT32(MF_MT_MPEG2_PROFILE, eAVEncH264VProfile_Base)) ||
        !check(transform_->SetOutputType(0, out.Get(), 0)) ||
        !check(transform_->SetInputType(0, in.Get(), 0)) ||
        !check(transform_->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0)) ||
        !check(transform_->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0)))
        return false;
    width_ = width; height_ = height; first_ = true;
    nv12_.resize(static_cast<size_t>(width) * height * 3 / 2);
    return true;
}

static uint8_t clip(int value) { return static_cast<uint8_t>(std::clamp(value, 0, 255)); }

bool H264Encoder::encode(const uint8_t* bgra, size_t pitch, int64_t time100ns,
                         std::vector<uint8_t>& annexB) {
    annexB.clear();
    if (!transform_ || !bgra || pitch < static_cast<size_t>(width_) * 4)
        return check(E_INVALIDARG);
    // BT.601 limited-range NV12. Chroma averages all four source pixels.
    for (unsigned y = 0; y < height_; y += 2) {
        for (unsigned x = 0; x < width_; x += 2) {
            int r = 0, g = 0, b = 0;
            for (unsigned dy = 0; dy < 2; ++dy) {
                const uint8_t* p = bgra + (y + dy) * pitch + x * 4;
                for (unsigned dx = 0; dx < 2; ++dx, p += 4) {
                    nv12_[static_cast<size_t>(y + dy) * width_ + x + dx] =
                        clip(((66 * p[2] + 129 * p[1] + 25 * p[0] + 128) >> 8) + 16);
                    r += p[2]; g += p[1]; b += p[0];
                }
            }
            size_t uv = static_cast<size_t>(width_) * height_ + (y / 2) * width_ + x;
            nv12_[uv] = clip(((-38 * r - 74 * g + 112 * b + 512) >> 10) + 128);
            nv12_[uv + 1] = clip(((112 * r - 94 * g - 18 * b + 512) >> 10) + 128);
        }
    }
    ComPtr<IMFSample> sample;
    ComPtr<IMFMediaBuffer> buffer;
    if (!check(MFCreateSample(&sample)) ||
        !check(MFCreateMemoryBuffer(static_cast<DWORD>(nv12_.size()), &buffer)))
        return false;
    BYTE* data = nullptr;
    if (!check(buffer->Lock(&data, nullptr, nullptr))) return false;
    memcpy(data, nv12_.data(), nv12_.size());
    buffer->Unlock();
    if (!check(buffer->SetCurrentLength(static_cast<DWORD>(nv12_.size()))) ||
        !check(sample->AddBuffer(buffer.Get())) || !check(sample->SetSampleTime(time100ns)) ||
        !check(sample->SetSampleDuration(10000000 / fps))) return false;
    if (first_) {
        VARIANT force{}; force.vt = VT_UI4; force.ulVal = 1;
        codec_->SetValue(&CODECAPI_AVEncVideoForceKeyFrame, &force);
    }
    // Synchronous, no B frames and low latency: consume each output before the
    // next input. No unbounded input queue and no dropping compressed deltas.
    if (!check(transform_->ProcessInput(0, sample.Get(), 0)) || !output(annexB)) return false;
    if (!annexB.empty()) first_ = false;
    return true;
}

bool H264Encoder::output(std::vector<uint8_t>& bytes) {
    for (unsigned attempt = 0; attempt < 16; ++attempt) {
        MFT_OUTPUT_STREAM_INFO info{};
        if (!check(transform_->GetOutputStreamInfo(0, &info))) return false;
        ComPtr<IMFSample> sample;
        if (!(info.dwFlags & MFT_OUTPUT_STREAM_PROVIDES_SAMPLES)) {
            ComPtr<IMFMediaBuffer> buffer;
            if (!check(MFCreateSample(&sample)) ||
                !check(MFCreateAlignedMemoryBuffer(std::max<DWORD>(info.cbSize, width_ * height_ * 2),
                                                   info.cbAlignment, &buffer)) ||
                !check(sample->AddBuffer(buffer.Get()))) return false;
        }
        MFT_OUTPUT_DATA_BUFFER out{};
        out.pSample = sample.Get();
        DWORD status = 0;
        HRESULT hr = transform_->ProcessOutput(0, 1, &out, &status);
        if (out.pEvents) out.pEvents->Release();
        if (!sample && out.pSample) sample.Attach(out.pSample);
        if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) return true;
        if (!check(hr) || !sample) return false;
        ComPtr<IMFMediaBuffer> buffer;
        if (!check(sample->ConvertToContiguousBuffer(&buffer))) return false;
        BYTE* data = nullptr; DWORD length = 0;
        if (!check(buffer->Lock(&data, nullptr, &length))) return false;
        // Bound output even if a codec unexpectedly returns oversized samples.
        const bool valid = length <= 16 * 1024 * 1024 && bytes.size() + length <= 16 * 1024 * 1024;
        if (valid) bytes.insert(bytes.end(), data, data + length);
        buffer->Unlock();
        if (!valid) return check(E_OUTOFMEMORY);
    }
    return check(E_UNEXPECTED);
}
