#include <dlfcn.h>
#include <stdio.h>
int main(int argc, char **argv) {
    if (argc < 2) { fprintf(stderr, "usage: %s <so>\n", argv[0]); return 2; }
    void *h = dlopen(argv[1], RTLD_LAZY);
    if (!h) { fprintf(stderr, "DLERR %s\n", dlerror()); return 1; }
    fprintf(stderr, "DLOK %p\n", h);
    void *init = dlsym(h, "UiTestExtension_OnInit");
    void *run = dlsym(h, "UiTestExtension_OnRun");
    fprintf(stderr, "INIT %p RUN %p\n", init, run);
    return 0;
}
