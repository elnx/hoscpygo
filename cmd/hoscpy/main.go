// Command hoscpy is a self-contained HarmonyOS mirroring and control server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"hoscpygo/internal/config"
	"hoscpygo/internal/hdc"
	"hoscpygo/internal/session"
	"hoscpygo/internal/webui"
)

var version = "dev"

// shutdownTimeout bounds the whole post-signal path. Cleanup talks to the
// device for fport removal and extension shutdown, and a wedged hdc connection
// must not keep the process alive after Ctrl-C.
const shutdownTimeout = 5 * time.Second

func main() {
	var (
		httpAddr  = flag.String("http", "", "HTTP listen address; giving -http/-https replaces the configured listeners (empty disables)")
		httpsAddr = flag.String("https", "", "HTTPS listen address with self-signed cert; giving -http/-https replaces the configured listeners (empty disables)")
		cfgPath   = flag.String("config", "", "config file path")
		hdcPath   = flag.String("hdc", "", "path to the hdc executable")
		server    = flag.String("s", "", "hdc server host[:port] to use exclusively (remote hdc machine)")
		noOpen    = flag.Bool("no-open", false, "do not open a browser")
		debug     = flag.Bool("debug", false, "enable debug logging")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	path := *cfgPath
	if path == "" {
		path = config.Path()
	}
	cfg, err := config.Load(path)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	if *hdcPath != "" {
		cfg.HdcPath = *hdcPath
	}
	if *server != "" {
		cfg.Server = *server
	}
	// flag.Visit distinguishes an absent flag from an explicit empty value.
	// Any explicit listener flag replaces the configured listener set: the
	// scheme whose flag is absent is disabled, so `-https 0.0.0.0:8899` does
	// not also bind a configured HTTP listener on the same address.
	var provided = map[string]bool{}
	flag.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	applyListenerFlags(cfg, *httpAddr, *httpsAddr, provided["http"], provided["https"])
	if *debug {
		cfg.Debug = true
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid listen configuration", "err", err)
		os.Exit(1)
	}
	if cfg.Server != "" {
		slog.Info("using hdc server override", "server", cfg.Server)
	}
	if err := cfg.Save(path); err != nil {
		slog.Warn("save config", "err", err)
	}

	bin, env, err := hdc.Resolve(cfg.HdcPath)
	if err != nil {
		slog.Error("resolve hdc", "err", err)
		os.Exit(1)
	}
	hdcClient := hdc.New(bin, env)
	hdcClient.Debug = cfg.Debug
	if v, err := hdcClient.Version(context.Background()); err == nil {
		slog.Info("hdc ready", "path", bin, "version", v)
	} else {
		slog.Warn("hdc version check failed", "path", bin, "err", err)
	}

	mgr := session.NewManager(cfg, path, hdcClient)
	ui := webui.New(cfg, path, mgr, hdcClient, version)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		<-sig
		slog.Info("shutting down")
		cancel()
		select {
		case <-sig:
			slog.Warn("forced shutdown")
		case <-done:
			return
		case <-time.After(shutdownTimeout):
			slog.Warn("shutdown timed out; exiting")
		}
		os.Exit(130)
	}()

	url := ""
	switch {
	case cfg.HTTPSListen != "":
		url = webui.HTTPSURL(cfg.HTTPSListen)
	case cfg.HTTPListen != "":
		url = webui.URL(cfg.HTTPListen)
	}
	if !*noOpen && cfg.AutoOpenBrowser {
		if err := openBrowser(url); err != nil {
			slog.Warn("open browser", "url", url, "err", err)
		}
	}
	slog.Info("hoscpy", "version", version, "os", runtime.GOOS, "arch", runtime.GOARCH,
		"http_listen", cfg.HTTPListen, "https_listen", cfg.HTTPSListen, "url", url)
	if err := ui.ListenAndServe(ctx); err != nil {
		slog.Error("http server", "err", err)
	}
	mgr.Close(context.Background())
	close(done)
}

// applyListenerFlags applies -http/-https overrides. Passing either flag
// replaces the whole configured listener set; the scheme whose flag is absent
// is disabled. This keeps `-https 0.0.0.0:8899` from also binding a configured
// HTTP listener on the same address.
func applyListenerFlags(cfg *config.Config, httpAddr, httpsAddr string, httpSet, httpsSet bool) {
	if !httpSet && !httpsSet {
		return
	}
	cfg.HTTPListen = ""
	cfg.HTTPSListen = ""
	if httpSet {
		cfg.HTTPListen = httpAddr
	}
	if httpsSet {
		cfg.HTTPSListen = httpsAddr
	}
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
