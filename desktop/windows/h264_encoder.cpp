// SPDX-License-Identifier: GPL-2.0-or-later
#include "h264_encoder.h"
#include <mfapi.h>
#include <mferror.h>
#include <wmcodecdsp.h>
#include <algorithm>
#include <cstring>
#include <cstdio>

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

H264Encoder::~H264Encoder() { close(); }

void H264Encoder::close() {
    events_.Reset();
    codec_.Reset();
    if (transform_) {
        ComPtr<IMFShutdown> shutdown;
        if (SUCCEEDED(transform_.As(&shutdown))) shutdown->Shutdown();
    }
    transform_.Reset();
    if (activation_) activation_->ShutdownObject();
    activation_.Reset();
    inputCredits_ = outputCredits_ = 0;
}

bool H264Encoder::init(unsigned width, unsigned height, bool preferHardware) {
    if (!width || !height || width > 4096 || height > 2160 || (width & 1) || (height & 1))
        return check(E_INVALIDARG);
    close();
    return (preferHardware && initHardware(width, height)) || initSoftware(width, height);
}

static std::string encoderName(IMFActivate* activation) {
    WCHAR value[256]{}; UINT32 length = 0;
    if (FAILED(activation->GetString(MFT_FRIENDLY_NAME_Attribute, value, 256, &length)))
        return "Windows-hardware-MFT";
    char utf8[1024]{};
    WideCharToMultiByte(CP_UTF8, 0, value, -1, utf8, sizeof(utf8), nullptr, nullptr);
    return utf8;
}

bool H264Encoder::initHardware(unsigned width, unsigned height) {
    MFT_REGISTER_TYPE_INFO in{MFMediaType_Video, MFVideoFormat_NV12};
    MFT_REGISTER_TYPE_INFO out{MFMediaType_Video, MFVideoFormat_H264};
    IMFActivate** candidates = nullptr; UINT32 count = 0;
    if (!check(MFTEnumEx(MFT_CATEGORY_VIDEO_ENCODER,
                        MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER,
                        &in, &out, &candidates, &count))) return false;
    bool ready = false;
    for (UINT32 i = 0; i < count && !ready; ++i) {
        close();
        activation_ = candidates[i];
        name_ = encoderName(candidates[i]);
        hardware_ = true;
        if (check(activation_->ActivateObject(IID_PPV_ARGS(&transform_)))) {
            ComPtr<IMFAttributes> attrs;
            ready = check(transform_->GetAttributes(&attrs)) &&
                    check(attrs->SetUINT32(MF_TRANSFORM_ASYNC_UNLOCK, TRUE)) &&
                    check(transform_.As(&events_)) && configure(width, height);
        }
        if (!ready)
            fprintf(stderr, "h264.hardware_unavailable encoder=%s hr=0x%08lx\n",
                    name(), static_cast<unsigned long>(error_));
    }
    for (UINT32 i = 0; i < count; ++i) candidates[i]->Release();
    CoTaskMemFree(candidates);
    if (!ready) close();
    return ready;
}

bool H264Encoder::initSoftware(unsigned width, unsigned height) {
    close();
    hardware_ = false;
    name_ = "Microsoft-software";
    if (!check(CoCreateInstance(CLSID_CMSH264EncoderMFT, nullptr, CLSCTX_INPROC_SERVER,
                               IID_PPV_ARGS(&transform_)))) return false;
    return configure(width, height);
}

bool H264Encoder::configure(unsigned width, unsigned height) {
    if (!check(transform_->QueryInterface(IID_ICodecAPI, reinterpret_cast<void**>(codec_.GetAddressOf()))))
        return false;
    DWORD inputs = 0, outputs = 0;
    if (!check(transform_->GetStreamCount(&inputs, &outputs))) return false;
    if (inputs != 1 || outputs != 1)
        return check(E_UNEXPECTED);
    inputId_ = outputId_ = 0;
    HRESULT ids = transform_->GetStreamIDs(1, &inputId_, 1, &outputId_);
    if (ids != E_NOTIMPL && !check(ids)) return false;
    auto setting = [&](const GUID& key, ULONG value) {
        VARIANT v{}; v.vt = VT_UI4; v.ulVal = value;
        return codec_->SetValue(&key, &v);
    };
    // CBR is a long-term target, not a hard cap on individual key frames.
    if (!check(setting(CODECAPI_AVEncCommonRateControlMode, eAVEncCommonRateControlMode_CBR)) ||
        !check(setting(CODECAPI_AVEncCommonMeanBitRate, bitrate)))
        return false;
    // Baseline forbids B frames even on drivers without this optional property.
    setting(CODECAPI_AVEncMPVDefaultBPictureCount, 0);
    VARIANT lowLatency{}; lowLatency.vt = VT_BOOL; lowLatency.boolVal = VARIANT_TRUE;
    HRESULT latency = codec_->SetValue(&CODECAPI_AVLowLatencyMode, &lowLatency);
    if (!hardware_ && !check(latency))
        return false;
    setting(CODECAPI_AVEncMPVGOPSize, fps * 2);
    setting(CODECAPI_AVEncCommonQualityVsSpeed, 20);
    ComPtr<IMFMediaType> out, in;
    if (!check(MFCreateMediaType(&out)) || !check(MFCreateMediaType(&in)) ||
        !videoType(out.Get(), MFVideoFormat_H264, width, height) ||
        !videoType(in.Get(), MFVideoFormat_NV12, width, height) ||
        !check(out->SetUINT32(MF_MT_AVG_BITRATE, bitrate)) ||
        !check(out->SetUINT32(MF_MT_MPEG2_PROFILE, eAVEncH264VProfile_Base)) ||
        !check(transform_->SetOutputType(outputId_, out.Get(), 0)) ||
        !check(transform_->SetInputType(inputId_, in.Get(), 0)))
        return false;
    // Hardware drivers can reset codec properties while media types are set.
    // Reapply rate control afterwards; a successful earlier SetValue is not
    // evidence that the running encoder retained the requested bitrate.
    if (!check(setting(CODECAPI_AVEncCommonRateControlMode, eAVEncCommonRateControlMode_CBR)) ||
        !check(setting(CODECAPI_AVEncCommonMeanBitRate, bitrate))) return false;
    setting(CODECAPI_AVEncMPVGOPSize, fps * 2);
    setting(CODECAPI_AVEncMPVDefaultBPictureCount, 0);
    codec_->SetValue(&CODECAPI_AVLowLatencyMode, &lowLatency);
    VARIANT actual{};
    if (SUCCEEDED(codec_->GetValue(&CODECAPI_AVEncCommonMeanBitRate, &actual))) {
        const bool mismatch = actual.vt == VT_UI4 && actual.ulVal != bitrate;
        fprintf(stderr, "h264.config encoder=%s target_bps=%u readback_bps=%lu\n",
                name(), bitrate, actual.vt == VT_UI4 ? actual.ulVal : 0UL);
        VariantClear(&actual);
        if (mismatch) return check(E_FAIL);
    }
    if (!check(transform_->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0)) ||
        !check(transform_->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0)))
        return false;
    width_ = width; height_ = height; first_ = true;
    nv12_.resize(static_cast<size_t>(width) * height * 3 / 2);
    ++generation_;
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
    if (submit(time100ns, annexB)) return true;
    if (!hardware_) return false;
    fprintf(stderr, "h264.hardware_failed encoder=%s hr=0x%08lx; retrying in software\n",
            name(), static_cast<unsigned long>(error_));
    // Discard the failed hardware stream, then encode this same picture as a
    // software IDR. generation() tells RFB to reset the client's decoder first.
    if (!initSoftware(width_, height_)) return false;
    annexB.clear();
    return submit(time100ns, annexB);
}

bool H264Encoder::waitCredit(bool input, ULONGLONG deadline) {
    while ((input ? inputCredits_ : outputCredits_) == 0) {
        if (GetTickCount64() >= deadline) return check(HRESULT_FROM_WIN32(WAIT_TIMEOUT));
        ComPtr<IMFMediaEvent> event;
        HRESULT hr = events_->GetEvent(MF_EVENT_FLAG_NO_WAIT, &event);
        if (hr == MF_E_NO_EVENTS_AVAILABLE) { Sleep(1); continue; }
        if (!check(hr)) return false;
        HRESULT status = S_OK;
        MediaEventType type = MEUnknown;
        if (!check(event->GetStatus(&status)) || !check(status) || !check(event->GetType(&type)))
            return false;
        if (type == METransformNeedInput) {
            UINT32 stream = inputId_;
            event->GetUINT32(MF_EVENT_MFT_INPUT_STREAM_ID, &stream);
            if (stream != inputId_ || inputCredits_ >= 32) return check(E_UNEXPECTED);
            ++inputCredits_;
        } else if (type == METransformHaveOutput) {
            if (outputCredits_ >= 32) return check(E_UNEXPECTED);
            ++outputCredits_;
        } else if (type == MEError) return check(E_FAIL);
    }
    return true;
}

bool H264Encoder::submit(int64_t time100ns, std::vector<uint8_t>& annexB) {
    // Event waits are bounded, including drivers that request more input but
    // never return the first picture. Never buffer additional desktop frames.
    const ULONGLONG deadline = GetTickCount64() + (first_ ? 1000 : 200);
    if (hardware_ && !waitCredit(true, deadline)) return false;
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
    if (!check(transform_->ProcessInput(inputId_, sample.Get(), 0))) return false;
    if (hardware_) {
        --inputCredits_;
#ifdef SVNC_H264_TESTING
        // Fail with a picture already in flight: teardown must discard that
        // stream and retry these pixels in software without duplicating output.
        if (failHardware_) { failHardware_ = false; return check(E_FAIL); }
#endif
        if (!waitCredit(false, deadline)) return false;
        --outputCredits_;
        if (!readOutput(annexB)) return false;
        if (annexB.empty()) return check(E_UNEXPECTED);
    } else if (!output(annexB)) return false;
    if (!annexB.empty()) first_ = false;
    return true;
}

bool H264Encoder::readOutput(std::vector<uint8_t>& bytes) {
    // A few drivers renegotiate their output type on the first output event.
    for (unsigned attempt = 0; attempt < 3; ++attempt) {
        MFT_OUTPUT_STREAM_INFO info{};
        if (!check(transform_->GetOutputStreamInfo(outputId_, &info))) return false;
        ComPtr<IMFSample> sample;
        if (!(info.dwFlags & MFT_OUTPUT_STREAM_PROVIDES_SAMPLES)) {
            ComPtr<IMFMediaBuffer> buffer;
            if (!check(MFCreateSample(&sample)) ||
                !check(MFCreateAlignedMemoryBuffer(std::max<DWORD>(info.cbSize, width_ * height_ * 2),
                                                   info.cbAlignment, &buffer)) ||
                !check(sample->AddBuffer(buffer.Get()))) return false;
        }
        MFT_OUTPUT_DATA_BUFFER out{};
        out.dwStreamID = outputId_;
        out.pSample = sample.Get();
        DWORD status = 0;
        HRESULT hr = transform_->ProcessOutput(0, 1, &out, &status);
        if (out.pEvents) out.pEvents->Release();
        if (!sample && out.pSample) sample.Attach(out.pSample);
        if (hr == MF_E_TRANSFORM_STREAM_CHANGE) {
            ComPtr<IMFMediaType> type;
            if (!check(transform_->GetOutputAvailableType(outputId_, 0, &type)) ||
                !check(transform_->SetOutputType(outputId_, type.Get(), 0))) return false;
            continue;
        }
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
        return true;
    }
    return check(E_UNEXPECTED);
}

bool H264Encoder::output(std::vector<uint8_t>& bytes) {
    for (unsigned attempt = 0; attempt < 16; ++attempt) {
        if (!readOutput(bytes)) return error_ == MF_E_TRANSFORM_NEED_MORE_INPUT;
    }
    return check(E_UNEXPECTED);
}
