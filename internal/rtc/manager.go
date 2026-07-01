package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"screen_server/internal/capture"
	"screen_server/internal/signaling"
	"screen_server/internal/sysinfo"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

const (
	videoTrackID     = "screen"
	videoStreamID    = "desktop"
	videoFrameBuffer = 4                       // 4 frames = 133ms at 30fps; absorbs startup & WebRTC jitter
	maxQueuedLatency = 150 * time.Millisecond  // 5 frames = 166ms; restart only on real backpressure
	statsLogEvery    = 60                      // frames between per-second aggregate timing logs
)

// timedFrame wraps an encoded frame with the timing metadata needed to
// instrument the Go-side pipeline (stdout read → channel → WriteSample).
type timedFrame struct {
	frame    capture.EncodedFrame
	seq      uint64
	tEnqueue time.Time // when placed onto the frames channel
	tReadUs  int64     // μs spent in AnnexBReader.ReadFrame()
}

type Manager struct {
	api           *webrtc.API
	captureCfg    capture.FFmpegConfig
	iceServers    []webrtc.ICEServer
	mu            sync.Mutex
	sessions      map[string]*Session
	pendingICE    map[string][]webrtc.ICECandidateInit
	maxPendingICE int
	dumpFrames    bool
	dumpDir       string
}

func NewManager(iceServers []webrtc.ICEServer, dumpFrames bool, dumpDir string) (*Manager, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640029",
		},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, fmt.Errorf("register h264 codec: %w", err)
	}

	interceptors := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, interceptors); err != nil {
		return nil, fmt.Errorf("register default interceptors: %w", err)
	}

	captureCfg := capture.DefaultFFmpegConfig()
	if input := os.Getenv("SCREEN_SERVER_CAPTURE_INPUT"); input != "" {
		captureCfg.Input = input
	}
	if device := os.Getenv("SCREEN_SERVER_CAPTURE_DEVICE"); device != "" {
		captureCfg.Device = device
	}
	if display := os.Getenv("SCREEN_SERVER_CAPTURE_DISPLAY"); display != "" {
		if display == "2" {
			captureCfg.Display = 2
		} else {
			captureCfg.Display = 1
		}
	}
	if fps := os.Getenv("SCREEN_SERVER_CAPTURE_FPS"); fps != "" {
		if parsed, err := strconv.Atoi(fps); err == nil && parsed > 0 {
			captureCfg.FPS = parsed
			// GOP will be recomputed below unless explicitly overridden.
		}
	}

	// Quality overrides — higher defaults than before, zero latency impact.
	if br := os.Getenv("SCREEN_SERVER_BITRATE"); br != "" {
		captureCfg.Bitrate = br
	}
	if mr := os.Getenv("SCREEN_SERVER_MAXRATE"); mr != "" {
		captureCfg.MaxRate = mr
	}
	if bs := os.Getenv("SCREEN_SERVER_BUFSIZE"); bs != "" {
		captureCfg.BufferSize = bs
	}
	if profile := os.Getenv("SCREEN_SERVER_PROFILE"); profile != "" {
		captureCfg.Profile = profile
	}

	// GOP: explicit override wins; otherwise 2×FPS gives a 2-second keyframe
	// interval which is fine for LAN packet loss and much more bitrate-efficient.
	if gop := os.Getenv("SCREEN_SERVER_GOP"); gop != "" {
		if parsed, err := strconv.Atoi(gop); err == nil && parsed > 0 {
			captureCfg.GOP = parsed
		}
	} else {
		captureCfg.GOP = max(1, captureCfg.FPS*2)
	}

	// Hardware encoding on Windows cuts the encode-stage latency and CPU load
	// without changing bitrate/quality/FPS. Probed once at startup.
	if runtime.GOOS == "windows" {
		captureCfg.Encoder = capture.ProbeEncoder(captureCfg.Binary)
		log.Printf("selected video encoder: %s", captureCfg.Encoder)
	}

	return &Manager{
		api: webrtc.NewAPI(
			webrtc.WithMediaEngine(mediaEngine),
			webrtc.WithInterceptorRegistry(interceptors),
		),
		captureCfg:    captureCfg,
		iceServers:    iceServers,
		sessions:      make(map[string]*Session),
		pendingICE:    make(map[string][]webrtc.ICECandidateInit),
		maxPendingICE: 32,
		dumpFrames:    dumpFrames,
		dumpDir:       dumpDir,
	}, nil
}

// ICEServerConfig is the JSON-serialisable form of ICE server config, used to
// relay credentials to the browser via the signaling channel.
type ICEServerConfig struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// GetICEServers returns the configured ICE servers in a form safe to send to the
// browser (credentials included, since this runs on a private signaling channel).
func (m *Manager) GetICEServers() []ICEServerConfig {
	out := make([]ICEServerConfig, 0, len(m.iceServers))
	for _, s := range m.iceServers {
		cred, _ := s.Credential.(string)
		out = append(out, ICEServerConfig{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: cred,
		})
	}
	return out
}

func (m *Manager) OnSignal(ctx context.Context, signal signaling.ServerSignal) {
	switch signal.Message.Type {
	case signaling.MessageTypeOffer:
		if err := m.handleOffer(ctx, signal); err != nil {
			log.Printf("rtc offer failed client=%s err=%v", signal.ClientID, err)
			signal.Send(signaling.Message{
				Type:    signaling.MessageTypeError,
				Payload: mustJSON(map[string]string{"message": err.Error()}),
			})
		}
	case signaling.MessageTypeCandidate:
		if err := m.handleCandidate(signal); err != nil {
			log.Printf("rtc candidate failed client=%s err=%v", signal.ClientID, err)
		}
	case signaling.MessageTypeInputMode:
		m.handleInputMode(signal)
	case signaling.MessageTypeQualityPreset:
		m.handleQualityPreset(signal)
	case signaling.MessageTypeCaptureSettings:
		m.handleCaptureSettings(signal)
	}
}

func (m *Manager) handleInputMode(signal signaling.ServerSignal) {
	var payload struct {
		CursorMode string `json:"cursorMode"`
	}
	if err := json.Unmarshal(signal.Message.Payload, &payload); err != nil {
		log.Printf("input-mode parse error client=%s err=%v", signal.ClientID, err)
		return
	}

	m.mu.Lock()
	session := m.sessions[signal.ClientID]
	m.mu.Unlock()

	if session == nil {
		return
	}

	cfg := m.captureCfg
	cfg.DrawMouse = payload.CursorMode == "remote-render"
	log.Printf("input-mode client=%s cursorMode=%s drawMouse=%v", signal.ClientID, payload.CursorMode, cfg.DrawMouse)

	// Non-blocking send to trigger capture restart
	select {
	case session.restartCh <- cfg:
	default:
		log.Printf("restart already pending for client=%s", signal.ClientID)
	}
}

// qualityPresets maps preset names to FFmpegConfig overrides.
var qualityPresets = map[string]struct {
	Bitrate, MaxRate, BufferSize string
	Profile                      string
	GOPFactor                    float64 // multiply by FPS; 0 means "keep current"
	NvencPreset, X264Preset      string
}{
	"smooth": {
		Bitrate: "8M", MaxRate: "12M", BufferSize: "1M",
		Profile: "baseline", GOPFactor: 0.5,
		NvencPreset: "p1", X264Preset: "ultrafast",
	},
	"balanced": {
		Bitrate: "20M", MaxRate: "30M", BufferSize: "10M",
		Profile: "high", GOPFactor: 2,
		NvencPreset: "p2", X264Preset: "superfast",
	},
	"quality": {
		Bitrate: "30M", MaxRate: "40M", BufferSize: "20M",
		Profile: "high", GOPFactor: 3,
		NvencPreset: "p4", X264Preset: "veryfast",
	},
}

func (m *Manager) handleQualityPreset(signal signaling.ServerSignal) {
	var payload struct {
		Preset string `json:"preset"`
	}
	if err := json.Unmarshal(signal.Message.Payload, &payload); err != nil {
		log.Printf("quality-preset parse error client=%s err=%v", signal.ClientID, err)
		return
	}

	p, ok := qualityPresets[payload.Preset]
	if !ok {
		log.Printf("quality-preset unknown preset=%q client=%s", payload.Preset, signal.ClientID)
		return
	}

	m.mu.Lock()
	session := m.sessions[signal.ClientID]
	m.mu.Unlock()
	if session == nil {
		return
	}

	cfg := m.captureCfg
	cfg.Bitrate = p.Bitrate
	cfg.MaxRate = p.MaxRate
	cfg.BufferSize = p.BufferSize
	cfg.Profile = p.Profile
	if p.GOPFactor > 0 {
		cfg.GOP = max(1, int(float64(cfg.FPS)*p.GOPFactor))
	}
	cfg.NvencPreset = p.NvencPreset
	cfg.X264Preset = p.X264Preset

	log.Printf("quality-preset client=%s preset=%s bitrate=%s profile=%s gop=%d nvenc=%s x264=%s",
		signal.ClientID, payload.Preset, cfg.Bitrate, cfg.Profile, cfg.GOP, cfg.NvencPreset, cfg.X264Preset)

	select {
	case session.restartCh <- cfg:
	default:
		log.Printf("restart already pending for client=%s", signal.ClientID)
	}
}

func (m *Manager) handleCaptureSettings(signal signaling.ServerSignal) {
	var payload struct {
		SessionID            int    `json:"sessionId"`
		CaptureMode          string `json:"captureMode"`
		DisplayIndex         int    `json:"displayIndex"`
		WindowTitle          string `json:"windowTitle"`
		WindowTransparencyBg string `json:"windowTransparencyBg"`
	}
	if err := json.Unmarshal(signal.Message.Payload, &payload); err != nil {
		log.Printf("[capture] parse error client=%s err=%v", signal.ClientID, err)
		return
	}

	log.Printf("[capture] settings-request client=%s mode=%s displayIdx=%d window=%q transp=%q sessionId=%d",
		signal.ClientID, payload.CaptureMode, payload.DisplayIndex, payload.WindowTitle,
		payload.WindowTransparencyBg, payload.SessionID)

	m.mu.Lock()
	session := m.sessions[signal.ClientID]
	m.mu.Unlock()
	if session == nil {
		log.Printf("[capture] no session for client=%s, ignoring settings", signal.ClientID)
		return
	}

	oldCfg := m.captureCfg
	cfg := m.captureCfg.Clone()
	cfg.CaptureMode = payload.CaptureMode
	cfg.WindowTitle = payload.WindowTitle
	cfg.WindowTransparencyBg = payload.WindowTransparencyBg

	// Log the state transition.
	log.Printf("[capture] state-transition client=%s oldMode=%s oldWindow=%q oldTransp=%q → newMode=%s newWindow=%q newTransp=%q",
		signal.ClientID, oldCfg.CaptureMode, oldCfg.WindowTitle, oldCfg.WindowTransparencyBg,
		cfg.CaptureMode, cfg.WindowTitle, cfg.WindowTransparencyBg)

	// Reject window mode with empty title — ffmpeg would fail and freeze.
	if payload.CaptureMode == "window" && payload.WindowTitle == "" {
		log.Printf("[capture] settings-rejected client=%s mode=window reason=\"empty window title\"", signal.ClientID)
		return
	}

	// Resolve display offset/size for display mode.
	if payload.CaptureMode == "display" {
		displays, err := sysinfo.EnumDisplays()
		if err != nil {
			log.Printf("[capture] enum-displays-failed client=%s err=%v", signal.ClientID, err)
		} else if payload.DisplayIndex >= 0 && payload.DisplayIndex < len(displays) {
			d := displays[payload.DisplayIndex]
			cfg.DisplayOffsetX = d.X
			cfg.DisplayOffsetY = d.Y
			cfg.DisplayWidth = d.Width
			cfg.DisplayHeight = d.Height
			log.Printf("[capture] display-resolved client=%s index=%d name=%q offset=(%d,%d) size=%dx%d primary=%v",
				signal.ClientID, payload.DisplayIndex, d.Name, d.X, d.Y, d.Width, d.Height, d.Primary)
		} else {
			log.Printf("[capture] display-index-oob client=%s index=%d numDisplays=%d", signal.ClientID, payload.DisplayIndex, len(displays))
		}
	}

	// Validate window exists on screen for window mode.
	if cfg.CaptureMode == "window" && cfg.WindowTitle != "" {
		x, y, w, h, found := sysinfo.GetWindowRectByTitle(cfg.WindowTitle)
		if found {
			log.Printf("[capture] window-found client=%s title=%q rect=(%d,%d %dx%d)",
				signal.ClientID, cfg.WindowTitle, x, y, w, h)
			// Reject minimized or too-small windows: Windows reports minimized
			// windows at (-32000,-32000) with 0×0 size. gdigrab will fail
			// with "Invalid properties, aborting" which kills the pipe and the
			// session. Also reject windows below NVENC minimum 64×64
			// resolution — the encoder fails with "Frame Dimension less than
			// the minimum" and crashes the session.
			if (w == 0 || h == 0) {
				log.Printf("[capture] settings-rejected client=%s mode=window title=%q reason=\"window is minimized (0x0 rect), restore it first\"",
					signal.ClientID, cfg.WindowTitle)
				return
			}
			if (w < 64 || h < 64) {
				log.Printf("[capture] settings-rejected client=%s mode=window title=%q reason=\"window size %dx%d below minimum 64x64 (likely minimized or offscreen), restore it first\"",
					signal.ClientID, cfg.WindowTitle, w, h)
				return
			}
		} else {
			log.Printf("[capture] window-not-found client=%s title=%q — capture may fail or produce blank frames",
				signal.ClientID, cfg.WindowTitle)
		}
	}

	log.Printf("[capture] settings-applied client=%s mode=%s displayIdx=%d window=%q transp=%q offset=%d,%d size=%dx%d",
		signal.ClientID, cfg.CaptureMode, payload.DisplayIndex, cfg.WindowTitle,
		cfg.WindowTransparencyBg, cfg.DisplayOffsetX, cfg.DisplayOffsetY,
		cfg.DisplayWidth, cfg.DisplayHeight)

	// Send capture-region to the client for input coordinate mapping.
	m.sendCaptureRegion(session, cfg)

	// Manage window-position polling so input mapping stays correct when the
	// user drags the captured window.
	if cfg.CaptureMode == "window" && cfg.WindowTitle != "" {
		log.Printf("[capture] start-window-poll client=%s window=%q", signal.ClientID, cfg.WindowTitle)
		m.startWindowPoll(session, cfg.WindowTitle)
	} else {
		m.stopWindowPoll(session)
	}

	// Drain any pending restart config, then send the new one.
	// Use non-blocking send so a dead capture pipe (readFrames exited due to
	// ffmpeg error) doesn't hang the handler goroutine.
	select {
	case old := <-session.restartCh:
		log.Printf("[capture] drained-stale-restart client=%s oldMode=%s oldWindow=%q", signal.ClientID, old.CaptureMode, old.WindowTitle)
	default:
	}
	select {
	case session.restartCh <- cfg:
		log.Printf("[capture] restart-queued client=%s mode=%s window=%q", signal.ClientID, cfg.CaptureMode, cfg.WindowTitle)
		// Update the manager-level config so the next handleCaptureSettings call
		// sees the correct "old" state in its state-transition log.
		m.captureCfg = cfg
	default:
		log.Printf("[capture] restart-failed client=%s reason=\"channel full or capture dead\" mode=%s window=%q",
			signal.ClientID, cfg.CaptureMode, cfg.WindowTitle)
	}
}

// sendCaptureRegion sends the current capture region (offset + size) to the
// client so it can map input coordinates from video space to screen space.
func (m *Manager) sendCaptureRegion(session *Session, cfg capture.FFmpegConfig) {
	var offsetX, offsetY, width, height int

	switch cfg.CaptureMode {
	case "display":
		offsetX = cfg.DisplayOffsetX
		offsetY = cfg.DisplayOffsetY
		width = cfg.DisplayWidth
		height = cfg.DisplayHeight
	case "window":
		if cfg.WindowTitle != "" {
			x, y, w, h, found := sysinfo.GetWindowRectByTitle(cfg.WindowTitle)
			if found {
				offsetX = x
				offsetY = y
				width = w
				height = h
			}
		}
		if width == 0 {
			vw, vh := sysinfo.GetVirtualScreenSize()
			width = vw
			height = vh
		}
	default: // "desktop"
		vw, vh := sysinfo.GetVirtualScreenSize()
		ox, oy := sysinfo.GetVirtualScreenOrigin()
		offsetX = ox
		offsetY = oy
		width = vw
		height = vh
	}

	vOriginX, vOriginY := sysinfo.GetVirtualScreenOrigin()
	log.Printf("[capture] region-sent client=%s mode=%s region=(%d,%d %dx%d) virtualOrigin=(%d,%d)",
		session.clientID, cfg.CaptureMode, offsetX, offsetY, width, height, vOriginX, vOriginY)

	session.send(signaling.Message{
		Type: signaling.MessageTypeCaptureRegion,
		Payload: mustJSON(map[string]int{
			"offsetX": offsetX,
			"offsetY": offsetY,
			"width":   width,
			"height":  height,
		}),
	})
}

// startWindowPoll starts a background goroutine that re-sends capture-region
// when the captured window moves.
func (m *Manager) startWindowPoll(session *Session, windowTitle string) {
	m.stopWindowPoll(session)

	pollCtx, cancel := context.WithCancel(session.ctx)
	session.windowPollCancel = cancel

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		defer log.Printf("window-poll stopped client=%s", session.clientID)

		lastX, lastY, lastW, lastH := -1, -1, -1, -1
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
			}
			x, y, w, h, found := sysinfo.GetWindowRectByTitle(windowTitle)
			if !found {
				continue
			}
			if x != lastX || y != lastY || w != lastW || h != lastH {
				log.Printf("window-poll client=%s window=%q moved: (%d,%d %dx%d) -> (%d,%d %dx%d)",
					session.clientID, windowTitle, lastX, lastY, lastW, lastH, x, y, w, h)
				lastX, lastY, lastW, lastH = x, y, w, h
				session.send(signaling.Message{
					Type: signaling.MessageTypeCaptureRegion,
					Payload: mustJSON(map[string]int{
						"offsetX": x,
						"offsetY": y,
						"width":   w,
						"height":  h,
					}),
				})
			}
		}
	}()
}

// stopWindowPoll stops the window-position polling goroutine if active.
func (m *Manager) stopWindowPoll(session *Session) {
	if session.windowPollCancel != nil {
		session.windowPollCancel()
		session.windowPollCancel = nil
	}
}

func (m *Manager) OnDisconnect(clientID string) {
	m.mu.Lock()
	session := m.sessions[clientID]
	delete(m.sessions, clientID)
	delete(m.pendingICE, clientID)
	m.mu.Unlock()
	if session != nil {
		session.Close()
	}
}

func (m *Manager) handleOffer(ctx context.Context, signal signaling.ServerSignal) error {
	var offer webrtc.SessionDescription
	if err := json.Unmarshal(signal.Message.Payload, &offer); err != nil {
		return fmt.Errorf("decode offer: %w", err)
	}

	session, err := m.newSession(signal.ClientID, signal.Room, signal.Send)
	if err != nil {
		return err
	}

	m.mu.Lock()
	var oldSessions []*Session
	for id, oldSession := range m.sessions {
		oldSessions = append(oldSessions, oldSession)
		delete(m.sessions, id)
	}
	m.sessions[signal.ClientID] = session
	pending := append([]webrtc.ICECandidateInit(nil), m.pendingICE[signal.ClientID]...)
	delete(m.pendingICE, signal.ClientID)
	m.mu.Unlock()

	// The current FFmpeg/AVFoundation path is a single-capture pipeline. Running
	// multiple screen captures at once makes macOS fall back or stall, so a new
	// viewer takes over the single active session. A future shared capture fanout
	// can support multiple viewers without duplicating the OS capture source.
	for _, oldSession := range oldSessions {
		oldSession.Close()
	}

	if err := session.pc.SetRemoteDescription(offer); err != nil {
		session.Close()
		return fmt.Errorf("set remote description: %w", err)
	}
	for _, candidate := range pending {
		if err := session.pc.AddICECandidate(candidate); err != nil {
			log.Printf("add pending ice failed client=%s err=%v", signal.ClientID, err)
		}
	}

	answer, err := session.pc.CreateAnswer(nil)
	if err != nil {
		session.Close()
		return fmt.Errorf("create answer: %w", err)
	}
	if err := session.pc.SetLocalDescription(answer); err != nil {
		session.Close()
		return fmt.Errorf("set local description: %w", err)
	}
	if local := session.pc.LocalDescription(); local != nil {
		signal.Send(signaling.Message{
			Type:    signaling.MessageTypeAnswer,
			Payload: mustJSON(local),
		})
	}

	if err := session.Start(ctx, m.captureCfg); err != nil {
		session.Close()
		return err
	}

	// Send initial capture-region so the client knows the coordinate mapping.
	m.sendCaptureRegion(session, m.captureCfg)

	return nil
}

func (m *Manager) handleCandidate(signal signaling.ServerSignal) error {
	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal(signal.Message.Payload, &candidate); err != nil {
		return fmt.Errorf("decode ice candidate: %w", err)
	}

	m.mu.Lock()
	session := m.sessions[signal.ClientID]
	if session == nil {
		pending := append(m.pendingICE[signal.ClientID], candidate)
		if len(pending) > m.maxPendingICE {
			pending = pending[len(pending)-m.maxPendingICE:]
		}
		m.pendingICE[signal.ClientID] = pending
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	return session.pc.AddICECandidate(candidate)
}

type Session struct {
	clientID string
	room     string
	send     func(signaling.Message)
	pc       *webrtc.PeerConnection
	track    *webrtc.TrackLocalStaticSample
	sender   *webrtc.RTPSender

	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
	done      chan struct{}
	restartCh chan capture.FFmpegConfig

	// windowPollCancel cancels the window-position polling goroutine used when
	// capture mode is "window". nil when not polling.
	windowPollCancel context.CancelFunc

	// Per-second timing accumulators (writeFrames goroutine only; single-writer,
	// no lock needed).
	frameSeq    uint64
	statCount   int64
	statReadUs  int64
	statQueueUs int64
	statWriteUs int64
	dumpWriter  *os.File
}

func (m *Manager) newSession(clientID, room string, send func(signaling.Message)) (*Session, error) {
	pc, err := m.api.NewPeerConnection(webrtc.Configuration{
		ICEServers: m.iceServers,
	})
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
	}

	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640029",
	}, videoTrackID, videoStreamID)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("create video track: %w", err)
	}

	sender, err := pc.AddTrack(track)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("add video track: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var dumpWriter *os.File
	if m.dumpFrames && m.dumpDir != "" {
		dumpPath := filepath.Join(m.dumpDir, clientID+".h264")
		var err error
		dumpWriter, err = os.Create(dumpPath)
		if err != nil {
			log.Printf("[dump] create file failed client=%s path=%s err=%v", clientID, dumpPath, err)
		} else {
			log.Printf("[dump] saving frames to %s", dumpPath)
		}
	}
	session := &Session{
		clientID:  clientID,
		room:      room,
		send:      send,
		pc:        pc,
		track:     track,
		sender:    sender,
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
		restartCh: make(chan capture.FFmpegConfig, 1),
		dumpWriter: dumpWriter,
	}

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		session.send(signaling.Message{
			Type:    signaling.MessageTypeCandidate,
			Payload: mustJSON(candidate.ToJSON()),
		})
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("rtc state client=%s state=%s", clientID, state.String())
		switch state {
		case webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateDisconnected,
			webrtc.PeerConnectionStateClosed:
			session.Close()
		}
	})

	return session, nil
}

func (s *Session) Start(parent context.Context, cfg capture.FFmpegConfig) error {
	stream, err := capture.StartFFmpegCapture(s.ctx, cfg)
	if err != nil {
		return fmt.Errorf("start h264 capture: %w", err)
	}

	frames := make(chan timedFrame, videoFrameBuffer)
	go s.readFrames(cfg, stream, frames)
	go s.writeFrames(frames)
	go s.readRTCP()

	select {
	case <-parent.Done():
		s.Close()
	default:
	}
	return nil
}

func (s *Session) Close() {
	s.once.Do(func() {
		s.cancel()
		if s.dumpWriter != nil { s.dumpWriter.Close() }
		_ = s.pc.Close()
		close(s.done)
	})
}

func (s *Session) readFrames(cfg capture.FFmpegConfig, stream *capture.FFmpegCapture, frames chan timedFrame) {
	defer close(frames)
	defer func() {
		if err := stream.Stop(); err != nil {
			log.Printf("stop capture failed client=%s err=%v", s.clientID, err)
		}
		// If we're exiting because of a capture error (not because Close() was
		// called), mark the session as dead so the Manager doesn't try to send
		// restart configs into a channel with no consumer.
		if s.ctx.Err() == nil {
			log.Printf("capture pipe died unexpectedly for client=%s, closing session", s.clientID)
			s.Close()
		}
	}()

	reader := stream.Reader()
	for {
		// Check for restart requests (non-blocking)
		select {
		case newCfg := <-s.restartCh:
			log.Printf("[capture] restart-begin client=%s oldMode=%s oldWindow=%q newMode=%s newWindow=%q newTransp=%q drawMouse=%v", s.clientID, cfg.CaptureMode, cfg.WindowTitle, newCfg.CaptureMode, newCfg.WindowTitle, newCfg.WindowTransparencyBg, newCfg.DrawMouse)
			if err := stream.Stop(); err != nil {
				log.Printf("[capture] restart-stop-err client=%s err=%v", s.clientID, err)
			}
			for len(frames) > 0 {
				<-frames
			}
			restarted, err := capture.StartFFmpegCapture(s.ctx, newCfg)
			startedCfg := newCfg
			if err != nil {
				// FFmpeg can fail to start if the target window is minimized or
				// otherwise invalid (e.g. gdigrab "Invalid properties, aborting").
				// Instead of killing the session, fall back to the old config so
				// the stream stays alive and the user can try another window.
				if s.ctx.Err() == nil {
					log.Printf("[capture] restart-start-failed client=%s newMode=%s newWindow=%q err=%v -- falling back to old config",
						s.clientID, newCfg.CaptureMode, newCfg.WindowTitle, err)
				}
				restarted, err = capture.StartFFmpegCapture(s.ctx, cfg)
				if err != nil {
					if s.ctx.Err() == nil {
						log.Printf("[capture] restart-fallback-failed client=%s err=%v", s.clientID, err)
					}
					return
				}
				startedCfg = cfg // fell back to old config
				log.Printf("[capture] restart-fallback-ok client=%s oldMode=%s oldWindow=%q",
					s.clientID, cfg.CaptureMode, cfg.WindowTitle)
			}
			stream = restarted
			reader = stream.Reader()
			cfg = startedCfg
			continue
		default:
		}

		select {
		case <-s.ctx.Done():
			return
		default:
		}

		// ---- Instrumented read ----
		tBeforeRead := time.Now()
		frame, err := reader.ReadFrame()
		tRead := time.Now()

		if err != nil {
			if !errors.Is(err, capture.ErrClosed) && s.ctx.Err() == nil {
				log.Printf("read h264 frame failed client=%s err=%v", s.clientID, err)
			}
			return
		}

		if len(frames)*int(frame.Duration) > int(maxQueuedLatency) {
			log.Printf("video queue exceeded latency budget client=%s queued=%d; restarting encoder for fresh IDR", s.clientID, len(frames))
			if err := stream.Stop(); err != nil {
				log.Printf("stop delayed capture failed client=%s err=%v", s.clientID, err)
			}
			for len(frames) > 0 {
				<-frames
			}
			restarted, err := capture.StartFFmpegCapture(s.ctx, cfg)
			if err != nil {
				if s.ctx.Err() == nil {
					log.Printf("restart capture failed client=%s err=%v", s.clientID, err)
				}
				return
			}
			stream = restarted
			reader = stream.Reader()
			continue
		}

		// H.264 P-frames reference earlier frames. Dropping arbitrary encoded
		// frames corrupts the decoder until the next IDR, which shows up as
		// tearing/smearing while dragging windows. Preserve bitstream continuity
		// and let backpressure reach FFmpeg; the encoder is already constrained
		// to the target FPS and Pion pacing keeps latency bounded.
		s.frameSeq++
		tf := timedFrame{
			frame:    frame,
			seq:      s.frameSeq,
			tEnqueue: time.Now(),
			tReadUs:  tRead.Sub(tBeforeRead).Microseconds(),
		}
		// Sample log every 120 frames (2s).
		if s.frameSeq%120 == 0 {
			log.Printf("[video] read seq=%d size=%d key=%v read_us=%d nalu=%v",
				tf.seq, len(tf.frame.Data), tf.frame.IsKeyframe, tf.tReadUs, tf.frame.NALUTypes)
		}

		select {
		case frames <- tf:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Session) writeFrames(frames <-chan timedFrame) {
	sentKeyframe := false
	written := 0
	keyframes := 0
	lastLog := time.Now()
	for {
		select {
		case <-s.ctx.Done():
			return
		case tf, ok := <-frames:
			if !ok {
				return
			}
			tGot := time.Now()
			if !sentKeyframe {
				if !tf.frame.IsKeyframe {
					continue
				}
				sentKeyframe = true
			}

			// ---- Instrumented write ----
			tBeforeWrite := time.Now()
			err := s.track.WriteSample(media.Sample{
				Data:     tf.frame.Data,
				Duration: tf.frame.Duration,
			})
			tAfterWrite := time.Now()

			if err != nil {
				if s.ctx.Err() == nil {
					log.Printf("write video sample failed client=%s err=%v", s.clientID, err)
					s.Close()
				}
				return
			}
			written++
			if tf.frame.IsKeyframe {
				keyframes++
			}

			// ---- Dump frames to disk when enabled ----
			// Every frame written to the WebRTC track is also appended to a
			// continuous .h264 file. The file is playable with ffplay and lets
			// you correlate log timestamps with what the client actually saw.
			if s.dumpWriter != nil {
				if _, werr := s.dumpWriter.Write(tf.frame.Data); werr != nil {
					log.Printf("[dump] write failed client=%s err=%v", s.clientID, werr)
					s.dumpWriter.Close()
					s.dumpWriter = nil
				}
			}

			// Accumulate per-second timing stats.
			s.statCount++
			s.statReadUs += tf.tReadUs
			s.statQueueUs += tGot.Sub(tf.tEnqueue).Microseconds()
			s.statWriteUs += tAfterWrite.Sub(tBeforeWrite).Microseconds()
			if s.statCount >= statsLogEvery {
				n := s.statCount
				readAvg := float64(s.statReadUs) / float64(n)
				queueAvg := float64(s.statQueueUs) / float64(n)
				writeAvg := float64(s.statWriteUs) / float64(n)
				goMs := float64(s.statReadUs+s.statQueueUs+s.statWriteUs) / 1000.0
				log.Printf("[video] stats n=%d read_avg=%.0fus queue_avg=%.0fus write_avg=%.0fus | go_total=%.1fms",
					n, readAvg, queueAvg, writeAvg, goMs)
				s.statCount = 0
				s.statReadUs = 0
				s.statQueueUs = 0
				s.statWriteUs = 0
			}

			if time.Since(lastLog) >= 2*time.Second {
				log.Printf(
					"video samples client=%s written=%d keyframes=%d last_bytes=%d last_keyframe=%v nalus=%v",
					s.clientID,
					written,
					keyframes,
					len(tf.frame.Data),
					tf.frame.IsKeyframe,
					tf.frame.NALUTypes,
				)
				lastLog = time.Now()
				written = 0
				keyframes = 0
			}
		}
	}
}

func (s *Session) readRTCP() {
	buf := make([]byte, 1500)
	for {
		n, _, err := s.sender.Read(buf)
		if err != nil {
			if s.ctx.Err() == nil {
				log.Printf("read rtcp failed client=%s err=%v", s.clientID, err)
			}
			return
		}
		packets, err := rtcp.Unmarshal(buf[:n])
		if err != nil {
			continue
		}
		for _, packet := range packets {
			switch packet.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				// Browsers request keyframes after packet loss or decoder recovery.
				// FFmpeg does not expose a cheap live keyframe trigger over stdout,
				// so we keep GOP short. Step 2's encoder emits an IDR at least once
				// per GOP; a future ScreenCaptureKit/VideoToolbox path can map this
				// callback to a real force-keyframe API.
				log.Printf("keyframe requested client=%s", s.clientID)
			}
		}
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
