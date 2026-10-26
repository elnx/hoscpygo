// Minimal uitest extension probe. The loader calls:
//   UiTestExtension_OnInit(void *lowLevelFunctions, int argc, char **argv)
//   UiTestExtension_OnRun(void)
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/prctl.h>
#include <unistd.h>

static void mark(const char *path, const char *msg) {
    int fd = open(path, O_CREAT | O_WRONLY | O_APPEND, 0644);
    if (fd >= 0) {
        dprintf(fd, "%s uid=%d euid=%d\n", msg, getuid(), geteuid());
        close(fd);
    }
    write(2, msg, strlen(msg));
    write(2, "\n", 1);
}

__attribute__((constructor))
static void probe_ctor(void) {
    prctl(PR_SET_NAME, "hoscpy_probe", 0, 0, 0);
    mark("/data/local/tmp/hoscpy_probe_ctor.log", "CTOR");
}

__attribute__((visibility("default")))
int UiTestExtension_OnInit(void *lowLevel, int argc, char **argv) {
    (void)lowLevel;
    mark("/data/local/tmp/hoscpy_probe_init.log", "INIT");
    int fd = open("/data/local/tmp/hoscpy_probe_argv.log", O_CREAT | O_WRONLY | O_APPEND, 0644);
    if (fd >= 0) {
        dprintf(fd, "argc=%d\n", argc);
        for (int i = 0; i < argc; ++i) dprintf(fd, "argv[%d]=%s\n", i, argv[i] ? argv[i] : "(null)");
        close(fd);
    }
    return 0;
}

__attribute__((visibility("default")))
void UiTestExtension_OnRun(void) {
    mark("/data/local/tmp/hoscpy_probe_run.log", "RUN");
    sleep(30);
}
