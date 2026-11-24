// SPDX-License-Identifier: GPL-2.0-or-later
#include "h264_encoder.h"
#include <algorithm>
#include <cerrno>
#include <climits>
#include <cstdio>
#include <cstring>
#include <unistd.h>
extern "C" {
#include <libavutil/opt.h>
}

H264Encoder::~H264Encoder() { close(); }
void H264Encoder::close() {
    sws_freeContext(convert_); convert_ = nullptr;
    av_packet_free(&packet_);
    av_frame_free(&hwFrame_);
    av_frame_free(&frame_);
    avcodec_free_context(&codec_);
    width_ = height_ = 0;
    hardware_ = false;
    first_ = true;
    lastPts_ = -1;
}
bool H264Encoder::check(int result) {
    if (result >= 0) return true;
    char message[AV_ERROR_MAX_STRING_SIZE]{};
    av_strerror(result, message, sizeof(message));
    error_ = message;
    return false;
}

bool H264Encoder::open(const char* encoder, unsigned width, unsigned height, const char* device) {
    close();
    const AVCodec* implementation = avcodec_find_encoder_by_name(encoder);
    if (!implementation) { error_ = "encoder is not installed"; return false; }
    codec_ = avcodec_alloc_context3(implementation);
    if (!codec_) return check(AVERROR(ENOMEM));
    const bool vaapi = !strcmp(encoder, "h264_vaapi");
    hardware_ = strcmp(encoder, "libx264") != 0;
    const AVPixelFormat softwareFormat = hardware_ ? AV_PIX_FMT_NV12 : AV_PIX_FMT_YUV420P;
    codec_->width = width; codec_->height = height;
    codec_->time_base = AVRational{1, 1000};
    codec_->framerate = AVRational{static_cast<int>(fps), 1};
    codec_->pix_fmt = vaapi ? AV_PIX_FMT_VAAPI : softwareFormat;
    codec_->bit_rate = codec_->rc_max_rate = bitrate;
    codec_->rc_buffer_size = bitrate / fps * 4;
    codec_->gop_size = fps * 2;
    codec_->max_b_frames = 0;
    codec_->refs = 1;
    codec_->thread_count = 2;
#if LIBAVCODEC_VERSION_MAJOR < 61
    codec_->profile = FF_PROFILE_H264_BASELINE;
#else
    codec_->profile = AV_PROFILE_H264_BASELINE;
#endif
    codec_->flags |= AV_CODEC_FLAG_LOW_DELAY;
    codec_->color_range = AVCOL_RANGE_MPEG;
    codec_->colorspace = AVCOL_SPC_BT709;
    codec_->color_primaries = AVCOL_PRI_BT709;
    codec_->color_trc = AVCOL_TRC_BT709;
    if (vaapi) {
        AVBufferRef* gpu = nullptr;
        if (!check(av_hwdevice_ctx_create(&gpu, AV_HWDEVICE_TYPE_VAAPI, device, nullptr, 0))) return false;
        codec_->hw_frames_ctx = av_hwframe_ctx_alloc(gpu);
        av_buffer_unref(&gpu);
        if (!codec_->hw_frames_ctx) return check(AVERROR(ENOMEM));
        auto* frames = reinterpret_cast<AVHWFramesContext*>(codec_->hw_frames_ctx->data);
        frames->format = AV_PIX_FMT_VAAPI;
        frames->sw_format = AV_PIX_FMT_NV12;
        frames->width = width; frames->height = height;
        frames->initial_pool_size = 4;
        if (!check(av_hwframe_ctx_init(codec_->hw_frames_ctx))) return false;
    }
    AVDictionary* options = nullptr;
    if (!hardware_) {
        av_dict_set(&options, "preset", "veryfast", 0);
        av_dict_set(&options, "tune", "zerolatency", 0);
        av_dict_set(&options, "x264-params", "repeat-headers=1:annexb=1:scenecut=0:rc-lookahead=0", 0);
    } else if (!vaapi) {
        av_dict_set(&options, "preset", "p1", 0);
        av_dict_set(&options, "tune", "ull", 0);
        av_dict_set(&options, "rc", "cbr", 0);
        av_dict_set(&options, "zerolatency", "1", 0);
        av_dict_set(&options, "rc-lookahead", "0", 0);
        av_dict_set(&options, "delay", "0", 0);
        av_dict_set(&options, "forced-idr", "1", 0);
    } else {
        av_dict_set(&options, "async_depth", "1", 0);
    }
    int result = avcodec_open2(codec_, implementation, &options);
    const bool unused = av_dict_count(options) != 0;
    av_dict_free(&options);
    if (!check(result)) return false;
    if (unused) { error_ = "encoder does not support the required low-latency options"; return false; }
    frame_ = av_frame_alloc(); packet_ = av_packet_alloc();
    if (!frame_ || !packet_) return check(AVERROR(ENOMEM));
    frame_->format = softwareFormat;
    frame_->width = width; frame_->height = height;
    frame_->color_range = codec_->color_range;
    frame_->colorspace = codec_->colorspace;
    frame_->color_primaries = codec_->color_primaries;
    frame_->color_trc = codec_->color_trc;
    if (!check(av_frame_get_buffer(frame_, 32))) return false;
    if (vaapi) {
        hwFrame_ = av_frame_alloc();
        if (!hwFrame_) return check(AVERROR(ENOMEM));
    }
    convert_ = sws_getContext(width, height, AV_PIX_FMT_BGRA, width, height,
                              softwareFormat, SWS_FAST_BILINEAR, nullptr, nullptr, nullptr);
    if (!convert_) return check(AVERROR(ENOMEM));
    const int* coefficients = sws_getCoefficients(SWS_CS_ITU709);
    if (!check(sws_setColorspaceDetails(convert_, coefficients, 1, coefficients, 0, 0, 1 << 16, 1 << 16))) return false;
    name_ = encoder;
    width_ = width; height_ = height;
    ++generation_;
    error_.clear();
    return true;
}

bool H264Encoder::init(unsigned width, unsigned height, bool preferHardware) {
    close();
    if (!width || !height || (width & 1) || (height & 1) || width > 4096 || height > 2160) {
        error_ = "H.264 requires even dimensions up to 4096x2160"; return false;
    }
    if (preferHardware) {
        if (open("h264_nvenc", width, height)) return true;
        // Probe render nodes accessible to this desktop user, without requiring
        // root or changing device permissions. VAAPI covers supported Intel/AMD GPUs.
        for (unsigned i = 128; i < 136; ++i) {
            char device[64]; snprintf(device, sizeof(device), "/dev/dri/renderD%u", i);
            if (!access(device, R_OK | W_OK) && open("h264_vaapi", width, height, device)) return true;
        }
    }
    if (open("libx264", width, height)) return true;
    close();
    return false;
}

static bool resetPicture(const std::vector<uint8_t>& bytes) {
    bool sps = false, pps = false, idr = false;
    for (size_t i = 0; i + 3 < bytes.size(); ++i) {
        if (bytes[i] || bytes[i + 1] || bytes[i + 2] != 1) continue;
        switch (bytes[i + 3] & 31) {
        case 7: sps = true; break;
        case 8: pps = true; break;
        case 5: idr = true; break;
        }
    }
    return sps && pps && idr;
}
bool H264Encoder::encodeFrame(const uint8_t* bgra, size_t pitch, int64_t timeMs, std::vector<uint8_t>& bytes) {
#ifdef SVNC_H264_TESTING
    if (hardware_ && failHardware_) { failHardware_ = false; return check(AVERROR(EIO)); }
#endif
    if (!check(av_frame_make_writable(frame_))) return false;
    const uint8_t* source[] = {bgra};
    const int strides[] = {static_cast<int>(pitch)};
    if (sws_scale(convert_, source, strides, 0, height_, frame_->data, frame_->linesize) != static_cast<int>(height_))
        return check(AVERROR(EINVAL));
    frame_->pts = std::max(timeMs, lastPts_ + 1);
    lastPts_ = frame_->pts;
    frame_->pict_type = first_ ? AV_PICTURE_TYPE_I : AV_PICTURE_TYPE_NONE;
    AVFrame* input = frame_;
    if (hwFrame_) {
        av_frame_unref(hwFrame_);
        if (!check(av_hwframe_get_buffer(codec_->hw_frames_ctx, hwFrame_, 0)) ||
            !check(av_hwframe_transfer_data(hwFrame_, frame_, 0)) ||
            !check(av_frame_copy_props(hwFrame_, frame_))) return false;
        input = hwFrame_;
    }
    if (!check(avcodec_send_frame(codec_, input))) return false;
    int result;
    while ((result = avcodec_receive_packet(codec_, packet_)) >= 0) {
        // A delayed picture must never be paired with the current RFB update.
        bool current = packet_->pts == frame_->pts && packet_->size > 0 &&
                       bytes.size() + packet_->size <= 16 * 1024 * 1024;
        if (current) bytes.insert(bytes.end(), packet_->data, packet_->data + packet_->size);
        av_packet_unref(packet_);
        if (!current) return check(AVERROR(EINVAL));
    }
    if (result != AVERROR(EAGAIN)) return check(result);
    if (bytes.empty() || (first_ && !resetPicture(bytes))) return check(AVERROR(ENOSYS));
    first_ = false;
    return true;
}
bool H264Encoder::encode(const uint8_t* bgra, size_t pitch, int64_t timeMs, std::vector<uint8_t>& bytes) {
    bytes.clear();
    if (!codec_ || !width_ || !bgra || pitch < width_ * 4 || pitch > INT_MAX) return check(AVERROR(EINVAL));
    if (encodeFrame(bgra, pitch, timeMs, bytes)) return true;
    if (!hardware_) return false;
    const unsigned width = width_, height = height_;
    fprintf(stderr, "h264.hardware_failed encoder=%s error=%s; retrying software IDR\n", name(), error());
    bytes.clear();
    return init(width, height, false) && encodeFrame(bgra, pitch, timeMs, bytes);
}
