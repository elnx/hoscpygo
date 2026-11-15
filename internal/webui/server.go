// Package webui serves the embedded browser UI and JSON/WebSocket APIs.
package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hoscpygo/internal/assets"
	"hoscpygo/internal/config"
	"hoscpygo/internal/hdc"
	"hoscpygo/internal/session"

	"github.com/gorilla/websocket"
)

type Server struct {
	cfg     *config.Config
	cfgPath string
	mgr     *session.Manager
	hdc     *hdc.Client
	web     fs.FS
	version string
	up      websocket.Upgrader
}

func New(cfg *config.Config, cfgPath string, mgr *session.Manager, h *hdc.Client, version string) *Server {
	return &Server{
		cfg: cfg, cfgPath: cfgPath, mgr: mgr, hdc: h, web: assets.Web(), version: version,
		up: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 512 * 1024,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				host := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
				if h, _, err := net.SplitHostPort(host); err == nil {
					host = h
				}
				return host == "127.0.0.1" || host == "localhost" || host == "::1" ||
					host == r.Host || (r.Host != "" && strings.HasPrefix(r.Host, host))
			},
		},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.web))))
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/devices", s.devices)
	mux.HandleFunc("POST /api/connect", s.connect)
	mux.HandleFunc("GET /api/session/{id}", s.sessionState)
	mux.HandleFunc("GET /api/session/{id}/size", s.sessionSize)
	mux.HandleFunc("GET /api/session/{id}/layout", s.sessionLayout)
	mux.HandleFunc("GET /api/session/{id}/screenshot", s.sessionScreenshot)
	mux.HandleFunc("POST /api/session/{id}/mode", s.sessionMode)
	mux.HandleFunc("POST /api/session/{id}/input", s.sessionInput)
	mux.HandleFunc("POST /api/session/{id}/disconnect", s.sessionDisconnect)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("POST /api/config", s.setConfig)
	mux.HandleFunc("GET /ws", s.ws)
	return logging(mux)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			slog.Debug("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).Round(time.Millisecond))
		}
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(s.web, "index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"ok": true, "version": s.version, "hdc": s.hdc.Bin,
	})
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	devices, errs := s.mgr.Devices(r.Context())
	if errs == nil {
		errs = []string{}
	}
	writeJSON(w, 200, map[string]any{"devices": devices, "errors": errs})
}

type connectReq struct {
	SN     string `json:"sn"`
	Server string `json:"server"`
	Mode   string `json:"mode"`
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	var req connectReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	sess, err := s.mgr.Connect(r.Context(), req.SN, req.Server, req.Mode)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"session": sess.ID, "state": sess.State()})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	id := r.PathValue("id")
	sess, ok := s.mgr.Get(id)
	if !ok {
		writeErr(w, 404, errors.New("session not found"))
	}
	return sess, ok
}

func (s *Server) sessionState(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.getSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, sess.State())
}

func (s *Server) sessionSize(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.getSession(w, r)
	if !ok {
		return
	}
	wid, hei := sess.Size(r.Context())
	writeJSON(w, 200, map[string]int{"width": wid, "height": hei})
}

func (s *Server) sessionLayout(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.getSession(w, r)
	if !ok {
		return
	}
	raw, err := sess.Layout(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) sessionScreenshot(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.getSession(w, r)
	if !ok {
		return
	}
	b, err := sess.Screenshot(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) sessionMode(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.getSession(w, r)
	if !ok {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := sess.SwitchMode(r.Context(), req.Mode); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, sess.State())
}

func (s *Server) sessionInput(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.getSession(w, r)
	if !ok {
		return
	}
	var msg session.InputMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := sess.HandleInput(r.Context(), msg); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) sessionDisconnect(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.getSession(w, r)
	if !ok {
		return
	}
	s.mgr.Disconnect(r.Context(), sess.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg)
}

func (s *Server) setConfig(w http.ResponseWriter, r *http.Request) {
	next := *s.cfg
	if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
		writeErr(w, 400, err)
		return
	}
	next.Normalize()
	if err := next.Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	*s.cfg = next
	if err := s.mgr.SaveConfig(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, s.cfg)
}

type wsError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	sess, ok := s.mgr.Get(id)
	if !ok {
		http.Error(w, "session not found", 404)
		return
	}
	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		slog.Debug("websocket upgrade failed", "err", err)
		return
	}
	conn.SetReadLimit(1 << 20)
	sub := sess.Subscribe()
	defer func() {
		sub.Close()
		_ = conn.Close()
	}()
	out := make(chan []byte, 16)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.Done():
				return
			case msg := <-sub.Messages():
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if msg.Binary {
					if err := conn.WriteMessage(websocket.BinaryMessage, msg.Data); err != nil {
						cancel()
						return
					}
				} else {
					if err := conn.WriteMessage(websocket.TextMessage, msg.Data); err != nil {
						cancel()
						return
					}
				}
			case b := <-out:
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					cancel()
					return
				}
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	// Initial state.
	if b, err := json.Marshal(sess.State()); err == nil {
		select {
		case out <- b:
		default:
		}
	}
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg session.InputMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type == "ping" {
			continue
		}
		if err := sess.HandleInput(ctx, msg); err != nil {
			slog.Debug("input failed", "session", sess.ID, "err", err)
			if b, err := json.Marshal(wsError{Type: "error", Message: err.Error()}); err == nil {
				select {
				case out <- b:
				default:
				}
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// ListenAndServe runs the configured HTTP and HTTPS listeners until ctx is
// done. HTTPS uses a generated self-signed certificate persisted next to the
// config file.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := s.cfg.Validate(); err != nil {
		return err
	}
	type endpoint struct {
		label    string
		addr     string
		url      string
		tls      bool
		certFile string
		keyFile  string
	}
	var endpoints []endpoint
	if s.cfg.HTTPListen != "" {
		endpoints = append(endpoints, endpoint{label: "http", addr: s.cfg.HTTPListen, url: URL(s.cfg.HTTPListen)})
	}
	if s.cfg.HTTPSListen != "" {
		certFile, keyFile, err := ensureSelfSignedCert(filepath.Join(filepath.Dir(s.cfgPath), "tls"), s.cfg.HTTPSListen)
		if err != nil {
			return fmt.Errorf("prepare TLS certificate: %w", err)
		}
		endpoints = append(endpoints, endpoint{
			label: "https", addr: s.cfg.HTTPSListen, url: HTTPSURL(s.cfg.HTTPSListen),
			tls: true, certFile: certFile, keyFile: keyFile,
		})
	}
	handler := s.Handler()
	servers := make([]*http.Server, len(endpoints))
	listeners := make([]net.Listener, len(endpoints))
	for i, ep := range endpoints {
		ln, err := net.Listen("tcp", ep.addr)
		if err != nil {
			for _, l := range listeners {
				if l != nil {
					_ = l.Close()
				}
			}
			return fmt.Errorf("%s listen %s: %w", ep.label, ep.addr, err)
		}
		listeners[i] = ln
		servers[i] = &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		slog.Info("web ui listening", "scheme", ep.label, "listen", ep.addr, "url", ep.url, "hdc", s.hdc.Bin)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, srv := range servers {
			_ = srv.Shutdown(shutdown)
		}
	}()

	errCh := make(chan error, len(servers))
	for i := range servers {
		srv, ln, ep := servers[i], listeners[i], endpoints[i]
		go func() {
			var err error
			if ep.tls {
				err = srv.ServeTLS(ln, ep.certFile, ep.keyFile)
			} else {
				err = srv.Serve(ln)
			}
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			errCh <- err
			if err != nil {
				cancel()
			}
		}()
	}
	var firstErr error
	for range servers {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// URL returns a browser-friendly HTTP URL for the listen address.
func URL(addr string) string {
	return schemeURL("http", addr)
}

// HTTPSURL returns a browser-friendly HTTPS URL for the listen address.
func HTTPSURL(addr string) string {
	return schemeURL("https", addr)
}

func schemeURL(scheme, addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return scheme + "://" + addr
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if _, err := strconv.Atoi(port); err != nil {
		port = "8899"
	}
	return scheme + "://" + host + ":" + port + "/"
}
