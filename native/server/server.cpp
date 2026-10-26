// uitest extension: ScrcpyService gRPC server with live screen capture.
//
// The protobuf and gRPC C++ runtimes are linked statically into this .so; the
// only dynamic dependencies are libc and the public OHOS capture/encoder
// libraries. The server is intentionally unsigned and uses the public
// OH_AVScreenCapture/OH_VideoEncoder APIs.

#include "capture.h"

#include <grpcpp/generic/callback_generic_service.h>
#include <grpcpp/security/server_credentials.h>
#include <grpcpp/server.h>
#include <grpcpp/server_builder.h>
#include <grpcpp/support/byte_buffer.h>
#include <grpcpp/support/slice.h>

#include <fcntl.h>
#include <sys/prctl.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

#include "scrcpy.pb.h"

namespace {

constexpr const char *kSocket = "unix-abstract:scrcpy_grpc_socket";
constexpr const char *kSamplePath = "/data/local/tmp/hoscpy_sample.h264";
constexpr const char *kLogPath = "/data/local/tmp/hoscpy_server.log";

std::mutex g_log_mu;
std::atomic<bool> g_stop{false};
std::unique_ptr<grpc::Server> g_server;

FrameQueue g_frames;
CapturePipeline g_capture(&g_frames);
CaptureOptions g_options;
std::mutex g_capture_mu;
bool g_capture_started = false;

void Log(const std::string &s) {
    std::lock_guard<std::mutex> lock(g_log_mu);
    int fd = open(kLogPath, O_CREAT | O_WRONLY | O_APPEND, 0644);
    if (fd >= 0) {
        dprintf(fd, "%s\n", s.c_str());
        close(fd);
    }
    fprintf(stderr, "%s\n", s.c_str());
}

std::vector<size_t> FindStartCodes(const std::vector<uint8_t> &data) {
    std::vector<size_t> out;
    for (size_t i = 0; i + 3 <= data.size(); ++i) {
        if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 1) {
            if (!out.empty() && out.back() + 3 == i) continue;
            out.push_back(i);
            i += 2;
        }
    }
    return out;
}

int NalTypeAt(const std::string &data, size_t start) {
    size_t p = start;
    while (p < data.size() && data[p] == 0) ++p;
    if (p + 1 >= data.size() || data[p] != 1) return -1;
    return static_cast<uint8_t>(data[p + 1]) & 0x1f;
}

bool ContainsNal(const std::string &data, int type) {
    std::vector<uint8_t> bytes(data.begin(), data.end());
    for (size_t start : FindStartCodes(bytes)) {
        if (NalTypeAt(data, start) == type) return true;
    }
    return false;
}

void LoadSampleFallback() {
    std::ifstream in(kSamplePath, std::ios::binary);
    if (!in) {
        Log(std::string("sample open failed: ") + kSamplePath);
        return;
    }
    std::vector<uint8_t> data((std::istreambuf_iterator<char>(in)),
                              std::istreambuf_iterator<char>());
    std::vector<size_t> starts = FindStartCodes(data);
    for (size_t i = 0; i < starts.size(); ++i) {
        size_t begin = starts[i];
        size_t end = (i + 1 < starts.size()) ? starts[i + 1] : data.size();
        if (end <= begin) continue;
        CapturedFrame frame;
        frame.data.assign(reinterpret_cast<const char *>(data.data() + begin),
                          end - begin);
        frame.keyframe = ((data[begin + 3] & 0x1f) == 5);
        g_frames.Push(std::move(frame));
    }
    Log("sample fallback: " + std::to_string(data.size()) + " bytes, " +
        std::to_string(starts.size()) + " NAL units");
}

bool EnsureCapture() {
    std::lock_guard<std::mutex> lock(g_capture_mu);
    if (g_capture_started) return true;
    if (g_capture.Start(g_options) != 0) return false;
    g_capture_started = true;
    return true;
}

std::string SerializeReply(const std::string &bytes, int32_t type) {
    ::ReplyMessage msg;
    msg.set_reply_type(type);
    ::ParamValue *pv = &(*msg.mutable_payload())["data"];
    pv->set_val_bytes(bytes);
    std::string out;
    msg.SerializeToString(&out);
    return out;
}

std::string SerializeEnd(int32_t result) {
    ::ReplyEndMessage msg;
    msg.set_result(result);
    std::string out;
    msg.SerializeToString(&out);
    return out;
}

class Reactor final : public grpc::ServerGenericBidiReactor {
  public:
    explicit Reactor(grpc::GenericCallbackServerContext *ctx)
        : ctx_(ctx), method_(ctx->method()) {
        StartRead(&request_);
    }

    void OnReadDone(bool ok) override {
        if (!ok) {
            Finish(grpc::Status::OK);
            return;
        }
        Log("call " + method_);
        if (method_ == "/ScrcpyService/onStart") {
            g_frames.Clear();
            param_data_.clear();
            sps_present_ = false;
            pps_present_ = false;
            idr_.clear();
            have_idr_ = false;
            bool live = EnsureCapture();
            if (!live) {
                LoadSampleFallback();
            } else {
                gate_keyframe_ = true;
            }
            worker_ = std::thread(&Reactor::StreamLoop, this);
            return;
        }
        if (method_ == "/ScrcpyService/onEnd") {
            {
                std::lock_guard<std::mutex> lock(g_capture_mu);
                if (g_capture_started) {
                    g_capture.Stop();
                    g_capture_started = false;
                }
            }
            response_ = SerializeEnd(0);
            grpc::Slice slice(response_.data(), response_.size());
            grpc::ByteBuffer buf(&slice, 1);
            StartWriteAndFinish(&buf, grpc::WriteOptions(), grpc::Status::OK);
            return;
        }
        if (method_ == "/ScrcpyService/onRequestIDRFrame") {
            g_capture.RequestKeyFrame();
            response_ = SerializeEnd(0);
            grpc::Slice slice(response_.data(), response_.size());
            grpc::ByteBuffer buf(&slice, 1);
            StartWriteAndFinish(&buf, grpc::WriteOptions(), grpc::Status::OK);
            return;
        }
        Finish(grpc::Status(grpc::StatusCode::UNIMPLEMENTED, "unknown method"));
    }

    void OnWriteDone(bool ok) override {
        std::lock_guard<std::mutex> lock(write_mu_);
        write_done_ = true;
        write_cv_.notify_all();
        if (!ok) stop_.store(true);
    }

    void OnDone() override {
        done_.store(true);
        stop_.store(true);
        {
            std::lock_guard<std::mutex> lock(write_mu_);
            write_cv_.notify_all();
        }
        if (worker_.joinable()) worker_.join();
        delete this;
    }

  private:
    void StreamLoop() {
        while (!stop_.load()) {
            CapturedFrame frame;
            if (!g_frames.Wait(&frame, stop_)) continue;
            if (gate_keyframe_.load()) {
                bool has_sps = ContainsNal(frame.data, 7);
                bool has_pps = ContainsNal(frame.data, 8);
                bool has_idr = frame.keyframe || ContainsNal(frame.data, 5);
                if (has_idr && has_sps && has_pps) {
                    // Self-contained IDR access unit.
                    gate_keyframe_.store(false);
                } else if (has_idr) {
                    idr_ = frame.data;
                    have_idr_ = true;
                    if (sps_present_ && pps_present_) {
                        frame.data = param_data_ + idr_;
                        gate_keyframe_.store(false);
                    } else {
                        continue;
                    }
                } else if (has_sps || has_pps) {
                    if (has_sps && has_pps) {
                        if (!sps_present_ || !pps_present_) {
                            param_data_ += frame.data;
                        }
                        sps_present_ = true;
                        pps_present_ = true;
                    } else if (has_sps) {
                        if (!sps_present_) {
                            param_data_ += frame.data;
                            sps_present_ = true;
                        }
                    } else {
                        if (!pps_present_) {
                            param_data_ += frame.data;
                            pps_present_ = true;
                        }
                    }
                    if (have_idr_ && sps_present_ && pps_present_) {
                        frame.data = param_data_ + idr_;
                        gate_keyframe_.store(false);
                    } else {
                        continue;
                    }
                } else {
                    continue;
                }
            }
            response_ = SerializeReply(frame.data, 1);
            grpc::Slice slice(response_.data(), response_.size());
            grpc::ByteBuffer buf(&slice, 1);
            {
                std::unique_lock<std::mutex> lock(write_mu_);
                write_done_ = false;
                StartWrite(&buf);
                write_cv_.wait(lock, [&] {
                    return write_done_ || stop_.load();
                });
            }
            if (stop_.load()) break;
        }
        if (!done_.load() && !finished_.exchange(true)) {
            Finish(grpc::Status::OK);
        }
    }

    grpc::GenericCallbackServerContext *ctx_;
    std::string method_;
    grpc::ByteBuffer request_;
    std::string response_;
    std::string param_data_;
    bool sps_present_ = false;
    bool pps_present_ = false;
    std::string idr_;
    bool have_idr_ = false;
    std::thread worker_;
    std::atomic<bool> stop_{false};
    std::atomic<bool> done_{false};
    std::atomic<bool> finished_{false};
    std::mutex write_mu_;
    std::condition_variable write_cv_;
    bool write_done_ = true;
    std::atomic<bool> gate_keyframe_{false};
};

class GenericScrcpyService final : public grpc::CallbackGenericService {
  public:
    grpc::ServerGenericBidiReactor *CreateReactor(
        grpc::GenericCallbackServerContext *ctx) override {
        return new Reactor(ctx);
    }
};

GenericScrcpyService g_service;

void ParseArgs(int argc, char **argv) {
    for (int i = 0; i + 1 < argc; ++i) {
        std::string key = argv[i] ? argv[i] : "";
        std::string val = argv[i + 1] ? argv[i + 1] : "";
        try {
            if (key == "-scale") {
                int scale = std::stoi(val);
                if (scale < 1) scale = 1;
                if (scale > 8) scale = 8;
                g_options.width = 2880 / scale;
                g_options.height = 1920 / scale;
            } else if (key == "-frameRate") {
                g_options.fps = std::stoi(val);
            } else if (key == "-bitRate") {
                g_options.bitrate = std::stoi(val);
            } else if (key == "-iFrameInterval") {
                g_options.iframe_interval = std::stoi(val);
            } else if (key == "-width") {
                g_options.width = std::stoi(val);
            } else if (key == "-height") {
                g_options.height = std::stoi(val);
            }
        } catch (...) {
            Log("bad argument " + key + " " + val);
        }
    }
}

void RunServer() {
    grpc::ServerBuilder builder;
    builder.RegisterCallbackGenericService(&g_service);
    builder.AddListeningPort(kSocket, grpc::InsecureServerCredentials());
    builder.SetMaxReceiveMessageSize(100 * 1024 * 1024);
    builder.SetMaxSendMessageSize(100 * 1024 * 1024);
    g_server = builder.BuildAndStart();
    if (!g_server) {
        Log("gRPC BuildAndStart failed");
        return;
    }
    Log(std::string("listening on ") + kSocket);
    g_server->Wait();
    Log("server stopped");
}

}  // namespace

extern "C" __attribute__((visibility("default"))) int
UiTestExtension_OnInit(void *low_level, int argc, char **argv) {
    (void)low_level;
    prctl(PR_SET_NAME, "hoscpy_server", 0, 0, 0);
    ParseArgs(argc, argv);
    std::string args;
    for (int i = 0; i < argc; ++i) {
        if (i) args += " ";
        args += argv[i] ? argv[i] : "(null)";
    }
    Log("init argc=" + std::to_string(argc) + " argv=" + args +
        " uid=" + std::to_string(getuid()) + " size=" +
        std::to_string(g_options.width) + "x" +
        std::to_string(g_options.height));
    std::thread(RunServer).detach();
    return 0;
}

extern "C" __attribute__((visibility("default"))) void
UiTestExtension_OnRun(void) {
    Log("run");
    while (!g_stop.load()) sleep(1);
}
