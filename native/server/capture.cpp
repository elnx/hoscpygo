#include "capture.h"

#include <cstdio>
#include <cstring>
#include <fcntl.h>
#include <sys/prctl.h>
#include <unistd.h>

namespace {
void CapLog(const std::string &s) {
    int fd = open("/data/local/tmp/hoscpy_capture.log",
                  O_CREAT | O_WRONLY | O_APPEND, 0644);
    if (fd >= 0) {
        dprintf(fd, "%s\n", s.c_str());
        close(fd);
    }
}
}  // namespace

void FrameQueue::Push(CapturedFrame frame) {
    std::lock_guard<std::mutex> lock(mu_);
    if (queue_.size() >= 120) queue_.pop_front();
    queue_.push_back(std::move(frame));
    cv_.notify_all();
}

bool FrameQueue::Wait(CapturedFrame *out, const std::atomic<bool> &stop) {
    std::unique_lock<std::mutex> lock(mu_);
    cv_.wait_for(lock, std::chrono::milliseconds(100), [&] {
        return !queue_.empty() || stop.load();
    });
    if (queue_.empty()) return false;
    *out = std::move(queue_.front());
    queue_.pop_front();
    return true;
}

void FrameQueue::Clear() {
    std::lock_guard<std::mutex> lock(mu_);
    queue_.clear();
}

CapturePipeline::~CapturePipeline() { Stop(); }

int CapturePipeline::Start(const CaptureOptions &options) {
    if (running_.load()) return 0;
    options_ = options;

    encoder_ = OH_VideoEncoder_CreateByMime(OH_AVCODEC_MIMETYPE_VIDEO_AVC);
    if (encoder_ == nullptr) {
        CapLog("OH_VideoEncoder_CreateByMime failed");
        return -1;
    }
    OH_AVCodecCallback callback = {
        .onError = &CapturePipeline::OnEncoderError,
        .onStreamChanged = &CapturePipeline::OnStreamChanged,
        .onNeedInputBuffer = &CapturePipeline::OnNeedInputBuffer,
        .onNewOutputBuffer = &CapturePipeline::OnNewOutputBuffer,
    };
    if (OH_VideoEncoder_RegisterCallback(encoder_, callback, this) != 0) {
        CapLog("OH_VideoEncoder_RegisterCallback failed");
        OH_VideoEncoder_Destroy(encoder_);
        encoder_ = nullptr;
        return -1;
    }

    OH_AVFormat *format = OH_AVFormat_Create();
    if (format == nullptr) {
        CapLog("OH_AVFormat_Create failed");
        OH_VideoEncoder_Destroy(encoder_);
        encoder_ = nullptr;
        return -1;
    }
    OH_AVFormat_SetIntValue(format, OH_MD_KEY_WIDTH, options_.width);
    OH_AVFormat_SetIntValue(format, OH_MD_KEY_HEIGHT, options_.height);
    OH_AVFormat_SetDoubleValue(format, OH_MD_KEY_FRAME_RATE, options_.fps);
    OH_AVFormat_SetLongValue(format, OH_MD_KEY_BITRATE, options_.bitrate);
    OH_AVFormat_SetIntValue(format, OH_MD_KEY_PIXEL_FORMAT, AV_PIXEL_FORMAT_RGBA);
    OH_AVFormat_SetIntValue(format, OH_MD_KEY_VIDEO_ENCODE_BITRATE_MODE,
                            BITRATE_MODE_VBR);
    OH_AVFormat_SetIntValue(format, OH_MD_KEY_PROFILE, AVC_PROFILE_MAIN);
    OH_AVFormat_SetIntValue(format, OH_MD_KEY_I_FRAME_INTERVAL,
                            options_.iframe_interval);
    if (OH_VideoEncoder_Configure(encoder_, format) != 0) {
        CapLog("OH_VideoEncoder_Configure failed");
        OH_AVFormat_Destroy(format);
        OH_VideoEncoder_Destroy(encoder_);
        encoder_ = nullptr;
        return -1;
    }
    OH_AVFormat_Destroy(format);

    if (OH_VideoEncoder_GetSurface(encoder_, &surface_) != 0 ||
        surface_ == nullptr) {
        CapLog("OH_VideoEncoder_GetSurface failed");
        OH_VideoEncoder_Destroy(encoder_);
        encoder_ = nullptr;
        return -1;
    }
    if (OH_VideoEncoder_Prepare(encoder_) != 0) {
        CapLog("OH_VideoEncoder_Prepare failed");
        OH_VideoEncoder_Destroy(encoder_);
        encoder_ = nullptr;
        surface_ = nullptr;
        return -1;
    }
    if (OH_VideoEncoder_Start(encoder_) != 0) {
        CapLog("OH_VideoEncoder_Start failed");
        OH_VideoEncoder_Destroy(encoder_);
        encoder_ = nullptr;
        surface_ = nullptr;
        return -1;
    }
    encoder_started_ = true;

    capture_ = OH_AVScreenCapture_Create();
    if (capture_ == nullptr) {
        CapLog("OH_AVScreenCapture_Create failed");
        Stop();
        return -1;
    }
    OH_VideoCaptureInfo cap = {};
    cap.videoFrameWidth = options_.width;
    cap.videoFrameHeight = options_.height;
    cap.videoSource = OH_VIDEO_SOURCE_SURFACE_RGBA;
    OH_VideoEncInfo enc = {};
    enc.videoCodec = OH_H264;
    enc.videoBitrate = options_.bitrate;
    enc.videoFrameRate = options_.fps;
    OH_AVScreenCaptureConfig config = {};
    config.captureMode = OH_CAPTURE_HOME_SCREEN;
    config.dataType = OH_ORIGINAL_STREAM;
    config.videoInfo.videoCapInfo = cap;
    config.videoInfo.videoEncInfo = enc;
    if (OH_AVScreenCapture_Init(capture_, config) != AV_SCREEN_CAPTURE_ERR_OK) {
        CapLog("OH_AVScreenCapture_Init failed");
        Stop();
        return -1;
    }
    OH_AVScreenCapture_SetMicrophoneEnabled(capture_, false);
    OH_AVScreenCapture_SetErrorCallback(capture_, &CapturePipeline::OnCaptureError,
                                        this);
    OH_AVScreenCapture_SetStateCallback(capture_, &CapturePipeline::OnCaptureState,
                                        this);
    if (OH_AVScreenCapture_StartScreenCaptureWithSurface(capture_, surface_) !=
        AV_SCREEN_CAPTURE_ERR_OK) {
        CapLog("OH_AVScreenCapture_StartScreenCaptureWithSurface failed");
        Stop();
        return -1;
    }
    capture_started_ = true;
    running_.store(true);
    CapLog("capture started " + std::to_string(options_.width) + "x" +
           std::to_string(options_.height) + " fps=" +
           std::to_string(options_.fps) + " bitrate=" +
           std::to_string(options_.bitrate));
    return 0;
}

void CapturePipeline::Stop() {
    running_.store(false);
    if (capture_ != nullptr) {
        if (capture_started_) OH_AVScreenCapture_StopScreenCapture(capture_);
        OH_AVScreenCapture_Release(capture_);
        capture_ = nullptr;
        capture_started_ = false;
    }
    if (encoder_ != nullptr) {
        if (encoder_started_) {
            OH_VideoEncoder_NotifyEndOfStream(encoder_);
            OH_VideoEncoder_Stop(encoder_);
            encoder_started_ = false;
        }
        OH_VideoEncoder_Destroy(encoder_);
        encoder_ = nullptr;
    }
    surface_ = nullptr;
    CapLog("capture stopped");
}

int CapturePipeline::RequestKeyFrame() {
    if (encoder_ == nullptr) return -1;
    OH_AVFormat *format = OH_AVFormat_Create();
    if (format == nullptr) return -1;
    OH_AVFormat_SetIntValue(format, OH_MD_KEY_REQUEST_I_FRAME, 1);
    int ret = OH_VideoEncoder_SetParameter(encoder_, format);
    OH_AVFormat_Destroy(format);
    return ret;
}

void CapturePipeline::OnCaptureError(OH_AVScreenCapture *, int32_t error,
                                     void *) {
    CapLog("capture error " + std::to_string(error));
}

void CapturePipeline::OnCaptureState(OH_AVScreenCapture *,
                                     OH_AVScreenCaptureStateCode state,
                                     void *) {
    CapLog("capture state " + std::to_string(static_cast<int>(state)));
}

void CapturePipeline::OnEncoderError(OH_AVCodec *, int32_t error, void *) {
    CapLog("encoder error " + std::to_string(error));
}

void CapturePipeline::OnStreamChanged(OH_AVCodec *, OH_AVFormat *format,
                                      void *) {
    int32_t w = 0, h = 0;
    if (format != nullptr) {
        OH_AVFormat_GetIntValue(format, OH_MD_KEY_WIDTH, &w);
        OH_AVFormat_GetIntValue(format, OH_MD_KEY_HEIGHT, &h);
    }
    CapLog("encoder stream " + std::to_string(w) + "x" + std::to_string(h));
}

void CapturePipeline::OnNeedInputBuffer(OH_AVCodec *, uint32_t, OH_AVBuffer *,
                                        void *) {
    // Surface input: the encoder owns the surface; no manual buffers.
}

void CapturePipeline::OnNewOutputBuffer(OH_AVCodec *codec, uint32_t index,
                                        OH_AVBuffer *buffer, void *user) {
    auto *self = static_cast<CapturePipeline *>(user);
    if (self == nullptr || buffer == nullptr) {
        OH_VideoEncoder_FreeOutputBuffer(codec, index);
        return;
    }
    OH_AVCodecBufferAttr attr = {};
    if (OH_AVBuffer_GetBufferAttr(buffer, &attr) != 0) {
        OH_VideoEncoder_FreeOutputBuffer(codec, index);
        return;
    }
    uint8_t *addr = OH_AVBuffer_GetAddr(buffer);
    if (addr != nullptr && attr.size > 0 &&
        !(attr.flags & AVCODEC_BUFFER_FLAGS_EOS)) {
        CapturedFrame frame;
        frame.data.assign(reinterpret_cast<char *>(addr + attr.offset),
                          attr.size);
        frame.keyframe = (attr.flags & AVCODEC_BUFFER_FLAGS_SYNC_FRAME) != 0;
        frame.pts = attr.pts;
        self->queue_->Push(std::move(frame));
    }
    OH_VideoEncoder_FreeOutputBuffer(codec, index);
}
