// Command hoscpygo is a self-contained HarmonyOS mirroring and control server.
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

	"hoscpygo/internal/config"
	"hoscpygo/internal/hdc"
	"hoscpygo/internal/session"
	"hoscpygo/internal/webui"
)

var version = "dev"

func main() {
	var (
		listen  = flag.String("listen", "", "HTTP listen address (default from config: 127.0.0.1:8899)")
		cfgPath = flag.String("config", "", "config file path")
		hdcPath = flag.String("hdc", "", "path to the hdc executable")
		noOpen  = flag.Bool("no-open", false, "do not open a browser")
		debug   = flag.Bool("debug", false, "enable debug logging")
		showVer = flag.Bool("version", false, "print version and exit")
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
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *hdcPath != "" {
		cfg.HdcPath = *hdcPath
	}
	if *debug {
		cfg.Debug = true
	}
	cfg.Normalize()
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
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		slog.Info("shutting down")
		cancel()
	}()

	url := webui.URL(cfg.Listen)
	if !*noOpen && cfg.AutoOpenBrowser {
		if err := openBrowser(url); err != nil {
			slog.Warn("open browser", "url", url, "err", err)
		}
	}
	slog.Info("hoscpygo", "version", version, "os", runtime.GOOS, "arch", runtime.GOARCH, "url", url)
	if err := ui.ListenAndServe(ctx); err != nil {
		slog.Error("http server", "err", err)
	}
	mgr.Close(context.Background())
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
