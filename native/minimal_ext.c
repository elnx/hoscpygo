// Minimal uitest extension probe. Records constructor + entry calls.
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static void logline(const char *path, const char *msg) {
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
    logline("/data/local/tmp/hoscpy_probe_ctor.log", "CTOR");
}

__attribute__((visibility("default")))
int UiTestExtension_OnInit(int argc, char **argv) {
    logline("/data/local/tmp/hoscpy_probe_init.log", "INIT");
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
    logline("/data/local/tmp/hoscpy_probe_run.log", "RUN");
    sleep(20);
}
