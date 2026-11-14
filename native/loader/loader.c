// hoscpy_loader: start the self-built unsigned server from a process we own.
//
// Why this exists
// ---------------
// uitest's extension loader applies the platform XPM check before dlopen on
// production devices: the unsigned server_static.z.so fails with
// "Permission denied" while signed vendor servers still load (and then fail
// relocation on newer OS versions). The same unsigned library loads fine from
// a normal process started by a root shell, so hoscpygo pushes this loader and
// runs it as root instead of going through uitest.
//
// The loader reproduces the only ABI uitest actually uses:
//
//   dlopen(server, RTLD_NOW | RTLD_GLOBAL)
//   UiTestExtension_OnInit(void *lowLevelFunctions, int argc, char **argv)
//   UiTestExtension_OnRun()
//
// UiTestExtension_OnRun blocks in the server (gRPC server + capture pipeline),
// so the process stays alive until hoscpygo kills it.
//
// Build (the binary is committed as internal/assets/device/hoscpy_loader):
//
//   $OHOS_NDK/llvm/bin/aarch64-unknown-linux-ohos-clang \
//     --target=aarch64-linux-ohos --sysroot=$OHOS_NDK/sysroot \
//     -O2 -s -o internal/assets/device/hoscpy_loader \
//     native/loader/loader.c -ldl
//
// Usage:
//
//   hoscpy_loader /data/local/tmp/hoscpy_server.z.so \
//     -scale 2 -frameRate 60 -bitRate 8388608 -p 5000 -iFrameInterval 2000
//
// The process command line keeps the server path, so hoscpygo's ps-based
// cleanup (pattern "hoscpy_server.z.so") kills this process too.
#include <dlfcn.h>
#include <stdio.h>
#include <unistd.h>

typedef int (*hoscpy_init_fn)(void *, int, char **);
typedef void (*hoscpy_run_fn)(void);

int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr, "usage: %s <server.so> [args...]\n", argv[0]);
        return 2;
    }
    if (geteuid() != 0) {
        fprintf(stderr, "hoscpy_loader: root is required for the standalone backend\n");
        return 3;
    }
    void *handle = dlopen(argv[1], RTLD_NOW | RTLD_GLOBAL);
    if (handle == NULL) {
        fprintf(stderr, "hoscpy_loader: dlopen %s: %s\n", argv[1], dlerror());
        return 1;
    }
    hoscpy_init_fn init = (hoscpy_init_fn)dlsym(handle, "UiTestExtension_OnInit");
    hoscpy_run_fn run = (hoscpy_run_fn)dlsym(handle, "UiTestExtension_OnRun");
    if (init == NULL || run == NULL) {
        fprintf(stderr, "hoscpy_loader: %s lacks UiTestExtension_OnInit/OnRun\n", argv[1]);
        dlclose(handle);
        return 1;
    }
    int rc = init(NULL, argc - 2, argv + 2);
    if (rc != 0) {
        fprintf(stderr, "hoscpy_loader: OnInit returned %d\n", rc);
        dlclose(handle);
        return rc;
    }
    run();
    dlclose(handle);
    return 0;
}
