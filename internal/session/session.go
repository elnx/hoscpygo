// Package session owns per-device capture and input state and fans frames out
// to browser clients.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"hoscpygo/internal/config"
	"hoscpygo/internal/hdc"
	"hoscpygo/internal/layout"
	"hoscpygo/internal/scrcpy"
	"hoscpygo/internal/uitest"
)

const (
	stateIdle      = "idle"
	stateStarting  = "starting"
	stateStreaming = "streaming"
	stateError     = "error"
	stateStopped   = "stopped"
)

// Device is one connectable target.
type Device struct {
	SN     string `json:"sn"`
	Server string `json:"server"`
	Remote bool   `json:"remote"`
}

// State is the JSON state message pushed to browsers.
type State struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	SN     string `json:"sn"`
	Server string `json:"server"`
	State  string `json:"state"`
	Mode   string `json:"mode"`
	Error  string `json:"error,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Scale  int    `json:"scale,omitempty"`
}

// InputMessage is a control message from the browser.
type InputMessage struct {
	Type       string `json:"type"`
	Event      string `json:"event"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Button     string `json:"button"`
	Dir        string `json:"dir"`
	Key        string `json:"key"`
	Shift      bool   `json:"shift"`
	Text       string `json:"text"`
	Horizontal bool   `json:"horizontal"`
	X2         int    `json:"x2"`
	Y2         int    `json:"y2"`
	Velocity   int    `json:"velocity"`
}

// Manager owns all sessions.
type Manager struct {
	cfg     *config.Config
	cfgPath string
	hdc     *hdc.Client
	mu      sync.Mutex
	byID    map[string]*Session
	byDev   map[string]*Session
}

func NewManager(cfg *config.Config, cfgPath string, h *hdc.Client) *Manager {
	return &Manager{cfg: cfg, cfgPath: cfgPath, hdc: h, byID: map[string]*Session{}, byDev: map[string]*Session{}}
}

func (m *Manager) Config() *config.Config { return m.cfg }

func (m *Manager) SaveConfig() error { return m.cfg.Save(m.cfgPath) }

// Devices lists targets on the local hdc server and all configured remotes.
func (m *Manager) Devices(ctx context.Context) ([]Device, []string) {
	type result struct {
		server string
		sns    []string
		err    error
	}
	servers := m.cfg.Servers()
	ch := make(chan result, len(servers))
	for _, server := range servers {
		server := server
		go func() {
			c, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			sns, err := m.hdc.ListTargets(c, server)
			ch <- result{server: server, sns: sns, err: err}
		}()
	}
	var out []Device
	var errs []string
	seen := map[string]bool{}
	for range servers {
		r := <-ch
		if r.err != nil {
			errs = append(errs, r.server+": "+r.err.Error())
			continue
		}
		host := hdc.TargetHost(r.server)
		remote := host != "127.0.0.1" && host != "localhost" && host != "::1"
		for _, sn := range r.sns {
			key := r.server + "/" + sn
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Device{SN: sn, Server: r.server, Remote: remote})
		}
	}
	return out, errs
}

// Connect creates (or reuses) a session and starts capture asynchronously.
func (m *Manager) Connect(ctx context.Context, sn, server, mode string) (*Session, error) {
	if sn == "" {
		return nil, errors.New("sn is required")
	}
	server = config.NormalizeServer(server)
	if server == "" {
		server = "127.0.0.1:8710"
	}
	key := server + "/" + sn
	m.mu.Lock()
	if s := m.byDev[key]; s != nil {
		m.mu.Unlock()
		if mode != "" && mode != s.State().Mode && mode != config.ModeAuto {
			go func() { _ = s.SwitchMode(context.Background(), mode) }()
		}
		return s, nil
	}
	if mode == "" {
		mode = m.cfg.Mode
	}
	id := newID()
	s := &Session{
		ID: id, SN: sn, Server: server, RequestedMode: mode,
		cfg: m.cfg, hdc: m.hdc,
		state:  stateIdle,
		hub:    NewHub(),
		uitest: uitest.New(m.hdc, server, sn),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	m.byID[id] = s
	m.byDev[key] = s
	m.mu.Unlock()
	s.hub.BroadcastJSON(s.State())
	go func() {
		if err := s.Start(s.ctx); err != nil {
			slog.Warn("session start failed", "sn", sn, "err", err)
		}
	}()
	return s, nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	return s, ok
}

func (m *Manager) Disconnect(ctx context.Context, id string) {
	m.mu.Lock()
	s := m.byID[id]
	if s != nil {
		delete(m.byID, id)
		delete(m.byDev, s.Server+"/"+s.SN)
	}
	m.mu.Unlock()
	if s != nil {
		s.Close(ctx)
	}
}

func (m *Manager) Close(ctx context.Context) {
	m.mu.Lock()
	list := make([]*Session, 0, len(m.byID))
	for _, s := range m.byID {
		list = append(list, s)
	}
	m.byID = map[string]*Session{}
	m.byDev = map[string]*Session{}
	m.mu.Unlock()
	for _, s := range list {
		s.Close(ctx)
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Session is one device mirror/control session.
type Session struct {
	ID            string
	SN            string
	Server        string
	RequestedMode string

	cfg *config.Config
	hdc *hdc.Client

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	state   string
	errMsg  string
	mode    string
	width   int
	height  int
	video   *scrcpy.Stream
	imageOn bool
	closed  bool

	uitest *uitest.Client
	hub    *Hub

	imgMu       sync.Mutex
	lastImage   []byte
	lastImageAt time.Time

	wheelMu   sync.Mutex
	wheelStop *time.Timer

	touchMu     sync.Mutex
	touchStart  time.Time
	touchStartX int
	touchStartY int
	touchLastX  int
	touchLastY  int
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{
		Type: "state", ID: s.ID, SN: s.SN, Server: s.Server,
		State: s.state, Mode: s.mode, Error: s.errMsg,
		Width: s.width, Height: s.height, Scale: s.cfg.Scale,
	}
}

func (s *Session) setState(state, mode, errMsg string) {
	s.mu.Lock()
	s.state = state
	if mode != "" {
		s.mode = mode
	}
	s.errMsg = errMsg
	st := State{
		Type: "state", ID: s.ID, SN: s.SN, Server: s.Server,
		State: s.state, Mode: s.mode, Error: s.errMsg,
		Width: s.width, Height: s.height, Scale: s.cfg.Scale,
	}
	s.mu.Unlock()
	s.hub.BroadcastJSON(st)
}

func (s *Session) setMode(mode string) {
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
}

func (s *Session) setError(err error) {
	s.setState(stateError, "", err.Error())
}

// Start tries the requested mode. In auto mode it falls back from video to
// image capture.
func (s *Session) Start(_ context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	s.state = stateStarting
	s.mu.Unlock()
	s.hub.BroadcastJSON(s.State())

	mode := s.RequestedMode
	if mode == "" {
		mode = config.ModeAuto
	}
	var videoErr error
	if mode == config.ModeAuto || mode == config.ModeVideo {
		if err := s.startVideo(); err == nil {
			return nil
		} else {
			videoErr = err
			slog.Warn("video start failed", "sn", s.SN, "err", err)
			s.setState(stateStarting, config.ModeImage, "video failed: "+err.Error())
		}
		if mode == config.ModeVideo {
			s.setError(videoErr)
			return videoErr
		}
	}
	if mode == config.ModeAuto || mode == config.ModeImage {
		if err := s.startImage(); err == nil {
			return nil
		} else {
			if videoErr != nil {
				err = fmt.Errorf("video: %v; image: %w", videoErr, err)
			}
			s.setError(err)
			return err
		}
	}
	err := fmt.Errorf("unsupported mode %q", mode)
	s.setError(err)
	return err
}

func (s *Session) startVideo() error {
	stream, err := scrcpy.Start(s.ctx, scrcpy.Options{
		HDC: s.hdc, Server: s.Server, SN: s.SN,
		Scale: s.cfg.Scale, FrameRate: s.cfg.FrameRate,
		BitRateMbps: s.cfg.BitRateMbps, IFrameInterval: s.cfg.IFrameInterval,
		Debug: s.cfg.Debug,
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		stream.Close()
		return errors.New("session closed")
	}
	s.video = stream
	s.mu.Unlock()
	s.setState(stateStreaming, config.ModeVideo, "")
	slog.Info("video streaming", "sn", s.SN, "variant", stream.Meta.Variant, "port", stream.Meta.Port)
	go s.consumeVideo(stream)
	return nil
}

func (s *Session) consumeVideo(stream *scrcpy.Stream) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case b, ok := <-stream.Frames:
			if !ok {
				return
			}
			s.hub.BroadcastBinary(b)
		case err, ok := <-stream.Errors:
			if ok && err != nil {
				s.setError(fmt.Errorf("video stream: %w", err))
				stream.Close()
			}
			return
		}
	}
}

func (s *Session) startImage() error {
	s.uitest = uitest.New(s.hdc, s.Server, s.SN)
	if err := s.uitest.Ensure(s.ctx); err != nil {
		return fmt.Errorf("uitest agent: %w", err)
	}
	first := make(chan struct{})
	var once sync.Once
	started, err := s.uitest.StartImage(s.ctx, s.cfg.ImageScale, func(jpg []byte) {
		once.Do(func() { close(first) })
		s.imgMu.Lock()
		s.lastImage = jpg
		s.lastImageAt = time.Now()
		s.imgMu.Unlock()
		s.hub.BroadcastBinary(jpg)
	}, func(err error) {
		s.setError(fmt.Errorf("image stream: %w", err))
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.imageOn = true
	s.mu.Unlock()
	select {
	case <-first:
		s.setState(stateStreaming, config.ModeImage, "")
		slog.Info("image streaming", "sn", s.SN, "scale", s.cfg.ImageScale)
		return nil
	case <-started:
		// The agent acknowledged but has not produced a frame yet (static
		// screen). Consider it started; the next change will paint.
		s.setState(stateStreaming, config.ModeImage, "")
		return nil
	case <-time.After(8 * time.Second):
		s.uitest.StopImage(context.Background())
		return errors.New("no image frame within 8s")
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// SwitchMode stops the current capture and starts another mode.
func (s *Session) SwitchMode(ctx context.Context, mode string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	s.mu.Unlock()
	s.stopCapture(ctx)
	s.RequestedMode = mode
	s.setState(stateStarting, "", "")
	return s.Start(ctx)
}

func (s *Session) stopCapture(ctx context.Context) {
	s.mu.Lock()
	stream := s.video
	s.video = nil
	imageOn := s.imageOn
	s.imageOn = false
	s.mu.Unlock()
	if stream != nil {
		stream.Close()
	}
	if imageOn {
		s.uitest.StopImage(ctx)
	}
}

func (s *Session) Close(ctx context.Context) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.state = stateStopped
	s.mu.Unlock()
	s.cancel()
	s.stopCapture(ctx)
	s.uitest.Close(ctx)
	if !s.cfg.KeepAgent {
		s.uitest.StopAgent(ctx)
	}
	s.hub.BroadcastJSON(s.State())
	s.hub.Close()
}

// Screenshot returns a JPEG of the current screen.
func (s *Session) Screenshot(ctx context.Context) ([]byte, error) {
	s.imgMu.Lock()
	if len(s.lastImage) > 0 && time.Since(s.lastImageAt) < 3*time.Second {
		b := append([]byte(nil), s.lastImage...)
		s.imgMu.Unlock()
		return b, nil
	}
	s.imgMu.Unlock()
	remote := fmt.Sprintf("/data/local/tmp/hoscpygo_shot_%d.jpeg", time.Now().UnixNano())
	local := filepath.Join(os.TempDir(), fmt.Sprintf("hoscpygo-shot-%d.jpeg", time.Now().UnixNano()))
	defer func() {
		_, _ = s.hdc.Shell(context.Background(), s.Server, s.SN, "rm -f "+remote, 5*time.Second)
		_ = os.Remove(local)
	}()
	out, err := s.hdc.Shell(ctx, s.Server, s.SN, "snapshot_display -f "+remote, 15*time.Second)
	if err != nil {
		return nil, err
	}
	if m := shotSizeRe.FindStringSubmatch(out); len(m) == 3 {
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		s.setSize(w, h)
	}
	if err := s.hdc.FileRecv(ctx, s.Server, s.SN, remote, local); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(local)
	if err != nil {
		return nil, err
	}
	s.imgMu.Lock()
	s.lastImage = b
	s.lastImageAt = time.Now()
	s.imgMu.Unlock()
	return b, nil
}

var shotSizeRe = regexp.MustCompile(`width:?\s*(\d+),\s*height:?\s*(\d+)`)

// Size returns the current device display size.
func (s *Session) Size(ctx context.Context) (int, int) {
	s.mu.Lock()
	if s.width > 0 && s.height > 0 {
		w, h := s.width, s.height
		s.mu.Unlock()
		return w, h
	}
	s.mu.Unlock()
	// snapshot_display is the most reliable source: it yields the actual
	// composited frame size, unlike Driver.getDisplaySize which may report the
	// maximum display size on foldables/windowed modes.
	if _, err := s.Screenshot(ctx); err == nil {
		s.mu.Lock()
		w, h := s.width, s.height
		s.mu.Unlock()
		if w > 0 && h > 0 {
			return w, h
		}
	}
	if raw, err := s.Layout(ctx); err == nil {
		if n, err := layout.Parse(raw); err == nil {
			w, h := n.ScreenSize()
			if w > 0 && h > 0 {
				s.setSize(w, h)
				return w, h
			}
		}
	}
	if w, h, err := s.uitest.DisplaySize(ctx); err == nil && w > 0 && h > 0 {
		s.setSize(w, h)
		return w, h
	}
	return 1260, 2720
}

func (s *Session) setSize(w, h int) {
	s.mu.Lock()
	s.width, s.height = w, h
	s.mu.Unlock()
	s.hub.BroadcastJSON(s.State())
}

// Layout returns the current view hierarchy.
func (s *Session) Layout(ctx context.Context) (json.RawMessage, error) {
	if err := s.uitest.Ensure(ctx); err != nil {
		return nil, err
	}
	raw, err := s.uitest.Layout(ctx)
	if err != nil {
		return nil, err
	}
	if n, err := layout.Parse(raw); err == nil {
		if w, h := n.ScreenSize(); w > 0 && h > 0 {
			s.setSize(w, h)
		}
	}
	return raw, nil
}

// HandleInput dispatches one browser input message.
func (s *Session) HandleInput(ctx context.Context, msg InputMessage) error {
	switch msg.Type {
	case "touch":
		return s.touchGesture(ctx, msg)
	case "tap":
		return s.uitest.Tap(ctx, msg.X, msg.Y)
	case "swipe":
		return s.uitest.Swipe(ctx, msg.X, msg.Y, msg.X2, msg.Y2, msg.Velocity)
	case "mouse":
		switch msg.Event {
		case "down":
			return s.uitest.MouseDown(ctx, msg.Button, msg.X, msg.Y)
		case "move":
			return s.uitest.MouseMove(ctx, msg.Button, msg.X, msg.Y)
		case "up":
			return s.uitest.MouseUp(ctx, msg.Button, msg.X, msg.Y)
		}
	case "wheel":
		return s.wheel(ctx, msg.Dir, msg.X, msg.Y)
	case "key":
		return s.key(ctx, msg.Key, msg.Shift)
	case "text":
		return s.text(ctx, msg.Text)
	case "rotate":
		if err := s.uitest.Rotate(ctx, msg.Horizontal); err != nil {
			return err
		}
		s.mu.Lock()
		w, h := s.width, s.height
		s.width, s.height = h, w
		s.mu.Unlock()
		_ = w
		return nil
	}
	return fmt.Errorf("unsupported input type %q", msg.Type)
}

func (s *Session) wheel(ctx context.Context, dir string, x, y int) error {
	delta := 500
	if dir == "up" {
		delta = -500
	}
	cmd := fmt.Sprintf("uinput -M -m %d %d -s %d", x, y, delta)
	_, err := s.hdc.Shell(ctx, s.Server, s.SN, cmd, 8*time.Second)
	return err
}

func (s *Session) touchGesture(ctx context.Context, msg InputMessage) error {
	switch msg.Event {
	case "down":
		s.touchMu.Lock()
		s.touchStart = time.Now()
		s.touchStartX, s.touchStartY = msg.X, msg.Y
		s.touchLastX, s.touchLastY = msg.X, msg.Y
		s.touchMu.Unlock()
		return nil
	case "move":
		s.touchMu.Lock()
		s.touchLastX, s.touchLastY = msg.X, msg.Y
		s.touchMu.Unlock()
		return nil
	case "up":
		s.touchMu.Lock()
		start := s.touchStart
		x1, y1 := s.touchStartX, s.touchStartY
		x2, y2 := s.touchLastX, s.touchLastY
		s.touchMu.Unlock()
		dx, dy := x2-x1, y2-y1
		dist := int(math.Sqrt(float64(dx*dx + dy*dy)))
		elapsed := time.Since(start)
		if dist < 16 && elapsed < 700*time.Millisecond {
			return s.uitest.Tap(ctx, x1, y1)
		}
		if dist < 16 && elapsed >= 700*time.Millisecond {
			return s.uitest.LongClick(ctx, x1, y1)
		}
		velocity := 600
		if elapsed > 0 {
			velocity = int(float64(dist) / elapsed.Seconds())
			if velocity < 200 {
				velocity = 200
			}
		}
		return s.uitest.Swipe(ctx, x1, y1, x2, y2, velocity)
	}
	return nil
}

func (s *Session) key(ctx context.Context, key string, shift bool) error {
	if key == "" {
		return nil
	}
	code, ok := keyCode(key, shift)
	if !ok {
		if len([]rune(key)) == 1 {
			return s.text(ctx, key)
		}
		return fmt.Errorf("unsupported key %q", key)
	}
	cmd := fmt.Sprintf("uinput -K -d %d -u %d", code, code)
	if shift {
		cmd = fmt.Sprintf("uinput -K -d 2047 -d %d -u %d -u 2047", code, code)
	}
	_, err := s.hdc.Shell(ctx, s.Server, s.SN, cmd, 8*time.Second)
	return err
}

func (s *Session) text(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	_, err := s.hdc.Shell(ctx, s.Server, s.SN, "uitest uiInput text "+hdc.Quote(text), 12*time.Second)
	return err
}

var keyCodes = map[string]int{
	"Back": 2, "Home": 1, "Power": 18, "VolumeUp": 16, "VolumeDown": 17, "Menu": 2067,
	"Backspace": 2055, "Enter": 2119, "ArrowUp": 2012, "ArrowDown": 2013,
	"ArrowLeft": 2014, "ArrowRight": 2015, " ": 2050, "Space": 2050,
	"=": 2058, "+": 2066, "-": 2057, "Escape": 2070, ";": 2062,
	"[": 2059, "]": 2060, "/": 2113, "'": 2063, ",": 2118, ".": 2117,
	"\\": 2061, "Tab": 2049, "`": 2056, "_": 2057, "!": 2001, "@": 2002,
	"#": 2003, "$": 2004, "%": 2005, "^": 2006, "&": 2007, "*": 2008,
	"(": 2009, ")": 2000, "{": 2059, "}": 2060, "|": 2061, ":": 2062,
	"\"": 2063, "<": 2118, ">": 2117, "?": 2113, "~": 2056,
}

func keyCode(key string, shift bool) (int, bool) {
	if code, ok := keyCodes[key]; ok {
		return code, true
	}
	if len(key) == 1 {
		c := key[0]
		switch {
		case c >= 'a' && c <= 'z':
			return 2017 + int(c-'a'), true
		case c >= 'A' && c <= 'Z':
			return 2017 + int(c-'A'), true
		case c >= '0' && c <= '9':
			return 2000 + int(c-'0'), true
		}
	}
	switch key {
	case "KeyA", "KeyB", "KeyC", "KeyD", "KeyE", "KeyF", "KeyG", "KeyH", "KeyI", "KeyJ",
		"KeyK", "KeyL", "KeyM", "KeyN", "KeyO", "KeyP", "KeyQ", "KeyR", "KeyS", "KeyT",
		"KeyU", "KeyV", "KeyW", "KeyX", "KeyY", "KeyZ":
		return 2017 + int(key[3]-'A'), true
	case "Digit0", "Digit1", "Digit2", "Digit3", "Digit4", "Digit5", "Digit6", "Digit7", "Digit8", "Digit9":
		return 2000 + int(key[5]-'0'), true
	}
	return 0, false
}

// Subscribe registers a fan-out subscriber for this session.
func (s *Session) Subscribe() *Subscriber {
	sub := s.hub.Subscribe()
	s.mu.Lock()
	video := s.video
	s.mu.Unlock()
	if video != nil {
		sub.SendJSON(map[string]any{"type": "video-reset"})
		go func() { _ = video.RequestIDR(context.Background()) }()
	}
	return sub
}
