package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"screen_server/internal/capture"
	"screen_server/internal/signaling"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

const (
	videoTrackID     = "screen"
	videoStreamID    = "desktop"
	videoFrameBuffer = 8
	maxQueuedLatency = 90 * time.Millisecond
)

type Manager struct {
	api           *webrtc.API
	captureCfg    capture.FFmpegConfig
	mu            sync.Mutex
	sessions      map[string]*Session
	pendingICE    map[string][]webrtc.ICECandidateInit
	maxPendingICE int
}

func NewManager() (*Manager, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
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
			captureCfg.GOP = max(1, parsed/4)
		}
	}

	return &Manager{
		api: webrtc.NewAPI(
			webrtc.WithMediaEngine(mediaEngine),
			webrtc.WithInterceptorRegistry(interceptors),
		),
		captureCfg:    captureCfg,
		sessions:      make(map[string]*Session),
		pendingICE:    make(map[string][]webrtc.ICECandidateInit),
		maxPendingICE: 32,
	}, nil
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
}

func (m *Manager) newSession(clientID, room string, send func(signaling.Message)) (*Session, error) {
	pc, err := m.api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
	}

	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
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

	frames := make(chan capture.EncodedFrame, videoFrameBuffer)
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
		_ = s.pc.Close()
		close(s.done)
	})
}

func (s *Session) readFrames(cfg capture.FFmpegConfig, stream *capture.FFmpegCapture, frames chan capture.EncodedFrame) {
	defer close(frames)
	defer func() {
		if err := stream.Stop(); err != nil {
			log.Printf("stop capture failed client=%s err=%v", s.clientID, err)
		}
	}()

	reader := stream.Reader()
	for {
		// Check for restart requests (non-blocking)
		select {
		case newCfg := <-s.restartCh:
			log.Printf("restarting capture for cursor mode change client=%s drawMouse=%v", s.clientID, newCfg.DrawMouse)
			if err := stream.Stop(); err != nil {
				log.Printf("stop capture for restart failed client=%s err=%v", s.clientID, err)
			}
			for len(frames) > 0 {
				<-frames
			}
			restarted, err := capture.StartFFmpegCapture(s.ctx, newCfg)
			if err != nil {
				if s.ctx.Err() == nil {
					log.Printf("restart capture failed client=%s err=%v", s.clientID, err)
				}
				return
			}
			stream = restarted
			reader = stream.Reader()
			cfg = newCfg
			continue
		default:
		}

		select {
		case <-s.ctx.Done():
			return
		default:
		}

		frame, err := reader.ReadFrame()
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
		select {
		case frames <- frame:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Session) writeFrames(frames <-chan capture.EncodedFrame) {
	sentKeyframe := false
	written := 0
	keyframes := 0
	lastLog := time.Now()
	var nextWrite time.Time
	for {
		select {
		case <-s.ctx.Done():
			return
		case frame, ok := <-frames:
			if !ok {
				return
			}
			if !sentKeyframe {
				if !frame.IsKeyframe {
					continue
				}
				sentKeyframe = true
			}
			if !nextWrite.IsZero() {
				delay := time.Until(nextWrite)
				if delay > 0 {
					timer := time.NewTimer(delay)
					select {
					case <-s.ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}
			if err := s.track.WriteSample(media.Sample{
				Data:     frame.Data,
				Duration: frame.Duration,
			}); err != nil {
				if s.ctx.Err() == nil {
					log.Printf("write video sample failed client=%s err=%v", s.clientID, err)
					s.Close()
				}
				return
			}
			if nextWrite.IsZero() || time.Since(nextWrite) > frame.Duration {
				nextWrite = time.Now().Add(frame.Duration)
			} else {
				nextWrite = nextWrite.Add(frame.Duration)
			}
			written++
			if frame.IsKeyframe {
				keyframes++
			}
			if time.Since(lastLog) >= 2*time.Second {
				log.Printf(
					"video samples client=%s written=%d keyframes=%d last_bytes=%d last_keyframe=%v nalus=%v",
					s.clientID,
					written,
					keyframes,
					len(frame.Data),
					frame.IsKeyframe,
					frame.NALUTypes,
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
