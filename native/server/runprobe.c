// Direct extension loader for probing without a uitest test-server/apiCaller.
// Build:
//   $OHOS_NDK/llvm/bin/aarch64-unknown-linux-ohos-clang \
//     --sysroot=$OHOS_NDK/sysroot -O2 -o runprobe runprobe.c -ldl
// Run:
//   ./runprobe /data/local/tmp/hoscpy_server.z.so -scale 1 -frameRate 60 \
//     -bitRate 8388608 -iFrameInterval 500
#include <dlfcn.h>
#include <stdio.h>

typedef int (*init_fn)(void *, int, char **);
typedef void (*run_fn)(void);

int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr, "usage: %s <so> [args...]\n", argv[0]);
        return 2;
    }
    void *handle = dlopen(argv[1], RTLD_NOW | RTLD_GLOBAL);
    if (handle == nullptr) {
        fprintf(stderr, "DLERR %s\n", dlerror());
        return 1;
    }
    fprintf(stderr, "DLOK %p\n", handle);
    init_fn init = (init_fn)dlsym(handle, "UiTestExtension_OnInit");
    run_fn run = (run_fn)dlsym(handle, "UiTestExtension_OnRun");
    fprintf(stderr, "SYMS init=%p run=%p\n", (void *)init, (void *)run);
    if (init == nullptr || run == nullptr) return 1;
    int rc = init(nullptr, argc - 2, argv + 2);
    fprintf(stderr, "INIT rc=%d\n", rc);
    if (rc != 0) return rc;
    run();
    return 0;
}
