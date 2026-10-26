// Public-NDK screen capture + H.264 encoder.
//
// The codec/capture sequence follows the public OH_AVScreenCapture and
// OH_VideoEncoder APIs. No Rosen/system-only headers are used.

#pragma once

#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <deque>
#include <functional>
#include <mutex>
#include <string>

#include <multimedia/player_framework/native_avbuffer.h>
#include <multimedia/player_framework/native_avcodec_base.h>
#include <multimedia/player_framework/native_avcodec_videoencoder.h>
#include <multimedia/player_framework/native_avformat.h>
#include <multimedia/player_framework/native_avscreen_capture.h>
#include <multimedia/player_framework/native_avscreen_capture_base.h>
#include <native_window/external_window.h>

struct CapturedFrame {
    std::string data;
    bool keyframe = false;
    int64_t pts = 0;
};

class FrameQueue {
  public:
    void Push(CapturedFrame frame);
    bool Wait(CapturedFrame *out, const std::atomic<bool> &stop);
    void Clear();

  private:
    std::mutex mu_;
    std::condition_variable cv_;
    std::deque<CapturedFrame> queue_;
};

struct CaptureOptions {
    int width = 2880;
    int height = 1920;
    int fps = 60;
    int bitrate = 8 * 1024 * 1024;
    int iframe_interval = 500;
};

class CapturePipeline {
  public:
    explicit CapturePipeline(FrameQueue *queue) : queue_(queue) {}
    ~CapturePipeline();

    int Start(const CaptureOptions &options);
    void Stop();
    int RequestKeyFrame();
    bool running() const { return running_.load(); }

  private:
    static void OnCaptureError(OH_AVScreenCapture *capture, int32_t error,
                               void *user);
    static void OnCaptureState(OH_AVScreenCapture *capture,
                               OH_AVScreenCaptureStateCode state, void *user);
    static void OnEncoderError(OH_AVCodec *codec, int32_t error, void *user);
    static void OnStreamChanged(OH_AVCodec *codec, OH_AVFormat *format,
                                void *user);
    static void OnNeedInputBuffer(OH_AVCodec *codec, uint32_t index,
                                  OH_AVBuffer *buffer, void *user);
    static void OnNewOutputBuffer(OH_AVCodec *codec, uint32_t index,
                                  OH_AVBuffer *buffer, void *user);

    FrameQueue *queue_;
    OH_AVCodec *encoder_ = nullptr;
    OHNativeWindow *surface_ = nullptr;
    OH_AVScreenCapture *capture_ = nullptr;
    CaptureOptions options_;
    std::atomic<bool> running_{false};
    bool encoder_started_ = false;
    bool capture_started_ = false;
};
