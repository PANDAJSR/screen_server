package capture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"screen_server/internal/sysinfo"
)

type FFmpegConfig struct {
	Binary     string
	Device     string
	Display    int
	Input      string
	FPS        int
	Bitrate    string
	MaxRate    string
	BufferSize string
	// GOP controls the maximum wait for decoder recovery after packet loss.
	// Step 3 should also listen for RTCP PLI/FIR; with an external FFmpeg
	// process the practical response is either a very short GOP or encoder
	// restart, because FFmpeg stdin is not a live force-keyframe control API.
	GOP         int
	UseHardware bool
	DrawMouse   bool // render cursor in captured video frames
	// Encoder selects the Windows H.264 encoder: "nvenc", "qsv", "amf", or
	// "x264" (software fallback). Empty/unknown ⇒ libx264. Hardware encoders
	// offload the encode stage from the CPU and cut end-to-end latency without
	// touching bitrate, FPS, or resolution.
	Encoder string
	// Profile selects the H.264 profile: "baseline", "main", or "high".
	// High profile enables CABAC and 8×8 transforms, giving ~15-20% better
	// compression at the same bitrate with zero added latency for modern
	// hardware decoders.
	Profile string
	// NvencPreset overrides the NVENC quality preset (p1–p7). p1=fastest,
	// p7=best quality. Default "p2" balances speed and compression.
	NvencPreset string
	// X264Preset overrides the libx264 speed preset. "ultrafast" is fastest,
	// "veryslow" is best quality. Default "superfast" for low-latency encoding.
	X264Preset string

	// ---- Capture mode settings ----

	// CaptureMode selects the capture target:
	//   "desktop" (default) — full virtual desktop across all monitors
	//   "display" — single monitor, positioned by DisplayOffsetX/Y
	//   "window"  — single window identified by WindowTitle
	CaptureMode string

	// For "display" mode: monitor position and size on the virtual desktop.
	DisplayOffsetX int
	DisplayOffsetY int
	DisplayWidth   int
	DisplayHeight  int

	// For "window" mode: target window title.
	WindowTitle string

	// WindowTransparencyBg replaces transparent/acrylic areas when capturing a
	// window: "" (keep as-is / see-through), "black", or "white".
	WindowTransparencyBg string
}

// Clone returns a shallow copy of the config. Value-types and strings are copied;
// slices (if any) are not shared since this struct has none.
func (c FFmpegConfig) Clone() FFmpegConfig {
	return c
}

func DefaultFFmpegConfig() FFmpegConfig {
	return FFmpegConfig{
		Binary:      "ffmpeg",
		Device:      defaultScreenDevice(),
		FPS:         30,
		Bitrate:     "20M",
		MaxRate:     "30M",
		BufferSize:  "10M",
		GOP:         60, // keyframe every 2s at 30fps; enough for LAN packet-loss recovery
		UseHardware: true,
		Profile:     "high",
		NvencPreset: "p2",
		X264Preset:  "superfast",
	}
}

func defaultScreenDevice() string {
	switch runtime.GOOS {
	case "darwin":
		return "3:none"
	default:
		return ":0.0"
	}
}

type FFmpegCapture struct {
	cfg    FFmpegConfig
	cmd    *exec.Cmd
	stdout io.ReadCloser
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

func StartFFmpegCapture(ctx context.Context, cfg FFmpegConfig) (*FFmpegCapture, error) {
	if cfg.Binary == "" {
		cfg.Binary = "ffmpeg"
	}
	if cfg.FPS <= 0 {
		cfg.FPS = 60
	}
	if cfg.GOP <= 0 {
		cfg.GOP = cfg.FPS
	}
	if cfg.Device == "" {
		cfg.Device = defaultScreenDevice()
	}
	if cfg.Input == "screencapture" && cfg.Display <= 0 {
		cfg.Display = displayFromDevice(cfg.Device)
	}

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, cfg.Binary, buildFFmpegArgs(cfg)...)
	var stdin io.WriteCloser
	if cfg.Input == "screencapture" {
		var err error
		stdin, err = cmd.StdinPipe()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("open ffmpeg stdin: %w", err)
		}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open ffmpeg stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open ffmpeg stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	c := &FFmpegCapture{
		cfg:    cfg,
		cmd:    cmd,
		stdout: stdout,
		cancel: cancel,
		done:   make(chan error, 1),
	}

	go logFFmpeg(stderr)
	if cfg.Input == "screencapture" {
		go feedScreencapture(runCtx, stdin, cfg)
	}
	go func() {
		err := cmd.Wait()
		if runCtx.Err() != nil {
			err = nil
		}
		c.done <- err
		close(c.done)
	}()

	log.Printf("[ffmpeg-start] mode=%s window=%q transp=%q input=%s fps=%d encoder=%s gop=%d bitrate=%s",
		cfg.CaptureMode, cfg.WindowTitle, cfg.WindowTransparencyBg,
		cfg.Input, cfg.FPS, cfg.Encoder, cfg.GOP, cfg.Bitrate)

	return c, nil
}

func (c *FFmpegCapture) Reader() *AnnexBReader {
	return NewAnnexBReader(c.stdout, c.cfg.FPS)
}

func (c *FFmpegCapture) Stop() error {
	c.once.Do(func() {
		c.cancel()
	})
	select {
	case err := <-c.done:
		return err
	case <-time.After(2 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		return <-c.done
	}
}

func buildFFmpegArgs(cfg FFmpegConfig) []string {
	if cfg.Input == "testsrc" {
		return buildTestSourceArgs(cfg)
	}
	if cfg.Input == "screencapture" {
		return buildImagePipeArgs(cfg)
	}
	switch runtime.GOOS {
	case "darwin":
		return buildDarwinArgs(cfg)
	case "windows":
		return buildWindowsArgs(cfg)
	default:
		return buildX11Args(cfg)
	}
}

func buildImagePipeArgs(cfg FFmpegConfig) []string {
	profile := cfg.Profile
	if profile == "" {
		profile = "high"
	}
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer",
		"-f", "image2pipe",
		"-framerate", fmt.Sprintf("%d", cfg.FPS),
		"-vcodec", "mjpeg",
		"-i", "pipe:0",
		"-an",
		"-vf", "format=nv12",
		"-c:v", "libx264",
		"-preset", x264Preset(cfg),
		"-tune", "zerolatency",
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", profile,
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	}
}

func buildTestSourceArgs(cfg FFmpegConfig) []string {
	profile := cfg.Profile
	if profile == "" {
		profile = "high"
	}
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer",
		"-f", "lavfi",
		"-re",
		"-i", fmt.Sprintf("testsrc2=size=1280x720:rate=%d", cfg.FPS),
		"-an",
		"-vf", "format=nv12",
		"-c:v", "libx264",
		"-preset", x264Preset(cfg),
		"-tune", "zerolatency",
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", profile,
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	}
}

func buildDarwinArgs(cfg FFmpegConfig) []string {
	codec := "libx264"
	if cfg.UseHardware {
		codec = "h264_videotoolbox"
	}
	profile := cfg.Profile
	if profile == "" {
		profile = "high"
	}
	captureCursor := "0"
	if cfg.DrawMouse {
		captureCursor = "1"
	}
	// Shared tail: bitrate / GOP / no B-frames / profile / AUD.
	sharedTail := []string{
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", profile,
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	}
	if cfg.UseHardware {
		return append([]string{
			"-hide_banner",
			"-loglevel", "warning",
			"-fflags", "nobuffer",
			"-f", "avfoundation",
			"-capture_cursor", captureCursor,
			"-capture_mouse_clicks", "0",
			"-framerate", fmt.Sprintf("%d", cfg.FPS),
			"-pixel_format", "bgr0",
			"-i", cfg.Device,
			"-an",
			"-vf", fmt.Sprintf("fps=%d,format=nv12", cfg.FPS),
			"-c:v", codec,
			"-realtime", "1",
			"-allow_sw", "1",
		}, sharedTail...)
	}
	return append([]string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer",
		"-f", "avfoundation",
		"-capture_cursor", captureCursor,
		"-capture_mouse_clicks", "0",
		"-framerate", fmt.Sprintf("%d", cfg.FPS),
		"-pixel_format", "bgr0",
		"-i", cfg.Device,
		"-an",
		"-vf", fmt.Sprintf("fps=%d,format=nv12", cfg.FPS),
		"-c:v", codec,
		"-preset", x264Preset(cfg),
		"-tune", "zerolatency",
	}, sharedTail...)
}

func buildX11Args(cfg FFmpegConfig) []string {
	drawMouse := "0"
	if cfg.DrawMouse {
		drawMouse = "1"
	}
	profile := cfg.Profile
	if profile == "" {
		profile = "high"
	}
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer",
		"-f", "x11grab",
		"-draw_mouse", drawMouse,
		"-framerate", fmt.Sprintf("%d", cfg.FPS),
		"-i", cfg.Device,
		"-an",
		"-c:v", "libx264",
		"-preset", x264Preset(cfg),
		"-tune", "zerolatency",
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", profile,
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	}
}

func buildWindowsArgs(cfg FFmpegConfig) []string {
	drawMouse := "0"
	if cfg.DrawMouse {
		drawMouse = "1"
	}
	profile := cfg.Profile
	if profile == "" {
		profile = "high"
	}

	// Window mode with transparency background: uses two inputs (window + color)
	// and filter_complex instead of -vf, so it needs a dedicated builder.
	if cfg.CaptureMode == "window" && cfg.WindowTitle != "" && cfg.WindowTransparencyBg != "" {
		return buildWindowsWindowTransparentArgs(cfg, drawMouse, profile)
	}

	// ---- Common input preamble ----
	common := []string{
		"-hide_banner",
		"-loglevel", "info",
		"-fflags", "nobuffer",
		"-thread_queue_size", "512",
		"-f", "gdigrab",
		"-framerate", fmt.Sprintf("%d", cfg.FPS),
		"-draw_mouse", drawMouse,
	}

	// Input target: desktop, desktop sub-region, or window.
	switch cfg.CaptureMode {
	case "display":
		// Capture a single monitor by its virtual-desktop offset and size.
		if cfg.DisplayWidth > 0 && cfg.DisplayHeight > 0 {
			common = append(common,
				"-offset_x", fmt.Sprintf("%d", cfg.DisplayOffsetX),
				"-offset_y", fmt.Sprintf("%d", cfg.DisplayOffsetY),
				"-video_size", fmt.Sprintf("%dx%d", cfg.DisplayWidth, cfg.DisplayHeight),
			)
		}
		common = append(common, "-i", "desktop")
	case "window":
		// Capture a specific window by title.
		common = append(common, "-i", fmt.Sprintf("title=%s", cfg.WindowTitle))
	default: // "desktop" / ""
		// Full virtual desktop (existing behavior).
		if size := os.Getenv("SCREEN_SERVER_CAPTURE_SIZE"); size != "" {
			common = append(common, "-video_size", size)
		}
		common = append(common, "-i", "desktop")
	}

	common = append(common, "-an", "-avioflags", "direct")

	// ---- Tail (bitrate, GOP, profile, output) ----
	tail := []string{
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", profile,
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	}

	enc := buildEncoderArgs(cfg)

	args := append(append(common, enc...), tail...)

	// Log key capture parameters for debugging window/display switching.
	log.Printf("[ffmpeg-args] mode=%s window=%q transp=%q offset=%d,%d size=%dx%d fps=%d encoder=%s gop=%d bitrate=%s args=%v",
		cfg.CaptureMode, cfg.WindowTitle, cfg.WindowTransparencyBg,
		cfg.DisplayOffsetX, cfg.DisplayOffsetY, cfg.DisplayWidth, cfg.DisplayHeight,
		cfg.FPS, cfg.Encoder, cfg.GOP, cfg.Bitrate, args)

	return args
}

// buildWindowsWindowTransparentArgs builds args for window capture with a solid
// background behind transparent/acrylic areas.
func buildWindowsWindowTransparentArgs(cfg FFmpegConfig, drawMouse, profile string) []string {
	bgColor := "black"
	if cfg.WindowTransparencyBg == "white" {
		bgColor = "white"
	}
	fps := fmt.Sprintf("%d", cfg.FPS)

	// Determine the window size so the background matches exactly.
	// gdigrab outputs the window at its native resolution; overlay at 0:0
	// on a matching-size background avoids any clipping or misalignment.
	winW, winH := 1920, 1080 // fallback if window rect lookup fails
	if cfg.WindowTitle != "" {
		if _, _, w, h, found := sysinfo.GetWindowRectByTitle(cfg.WindowTitle); found && w > 0 && h > 0 {
			winW, winH = w, h
		}
	}

	// Two inputs: [0] gdigrab window capture, [1] lavfi solid color.
	// filter_complex overlays window onto solid background.
	common := []string{
		"-hide_banner",
		"-loglevel", "info",
		"-fflags", "nobuffer",
		"-thread_queue_size", "512",
		"-f", "gdigrab",
		"-framerate", fps,
		"-draw_mouse", drawMouse,
		"-i", fmt.Sprintf("title=%s", cfg.WindowTitle),
		"-f", "lavfi",
		"-i", fmt.Sprintf("color=c=%s:s=%dx%d:r=%s", bgColor, winW, winH, fps),
		"-an",
		"-avioflags", "direct",
	}

	tail := []string{
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", profile,
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	}

	// Encoder args: use filter_complex instead of -vf. The filter_complex
	// overlays the window onto the color background, then passes through the
	// format conversion before encoding.
	enc := buildEncoderFilterComplex(cfg)

	// With filter_complex we use filter_complex output label for the encoder.
	// buildEncoderFilterComplex returns the filter_complex and codec args;
	// the filter_complex output pad is "[out]" which maps implicitly.
	result := append(common, enc...)
	result = append(result, tail...)

	log.Printf("[ffmpeg-args] mode=window+transp window=%q transp=%q fps=%s encoder=%s gop=%d bitrate=%s args=%v",
		cfg.WindowTitle, cfg.WindowTransparencyBg, fps, cfg.Encoder, cfg.GOP, cfg.Bitrate, result)

	return result
}

// buildEncoderFilterComplex returns filter_complex + encoder args for window
// mode with transparency background. The gdigrab window input is [0:v] and the
// color background input is [1:v].
func buildEncoderFilterComplex(cfg FFmpegConfig) []string {
	// Normalize to bgra first so gdigrab format switches (bgr0↔bgra, common
	// with console windows) don't break the downstream hardware filters.
	filter := "[1:v][0:v]overlay=0:0,format=bgra"
	switch cfg.Encoder {
	case "nvenc":
		nvencPreset := cfg.NvencPreset
		if nvencPreset == "" {
			nvencPreset = "p2"
		}
		return []string{
			"-filter_complex", filter + ",format=nv12,hwupload_cuda",
			"-c:v", "h264_nvenc",
			"-preset", nvencPreset,
			"-tune", "ll",
			"-rc", "cbr",
			"-delay", "0",
			"-spatial_aq", "1",
		}
	case "qsv":
		return []string{
			"-filter_complex", filter + ",format=nv12,hwupload=extra_hw_frames=16",
			"-c:v", "h264_qsv",
			"-preset", "veryfast",
			"-look_ahead", "0",
			"-adaptive_i", "1",
			"-adaptive_b", "1",
		}
	case "amf":
		return []string{
			"-filter_complex", filter + ",format=nv12",
			"-c:v", "h264_amf",
			"-usage", "lowlatency",
			"-quality", "balanced",
			"-rc", "cbr",
		}
	default: // x264
		x264Preset := cfg.X264Preset
		if x264Preset == "" {
			x264Preset = "superfast"
		}
		return []string{
			"-filter_complex", filter + ",format=yuv420p",
			"-c:v", "libx264",
			"-threads", "2",
			"-preset", x264Preset,
			"-tune", "zerolatency",
		}
	}
}

// buildEncoderArgs returns encoder-specific args (using -vf for simple filter).
// Prepends format=bgra to normalize gdigrab output, which can switch between
// bgr0 and bgra depending on window content (e.g. console windows).
func buildEncoderArgs(cfg FFmpegConfig) []string {
	switch cfg.Encoder {
	case "nvenc":
		nvencPreset := cfg.NvencPreset
		if nvencPreset == "" {
			nvencPreset = "p2"
		}
		return []string{
			"-vf", "format=bgra,format=nv12,hwupload_cuda",
			"-c:v", "h264_nvenc",
			"-preset", nvencPreset,
			"-tune", "ll",
			"-rc", "cbr",
			"-delay", "0",
			"-spatial_aq", "1",
		}
	case "qsv":
		return []string{
			"-vf", "format=bgra,format=nv12,hwupload=extra_hw_frames=16",
			"-c:v", "h264_qsv",
			"-preset", "veryfast",
			"-look_ahead", "0",
			"-adaptive_i", "1",
			"-adaptive_b", "1",
		}
	case "amf":
		return []string{
			"-vf", "format=bgra,format=nv12",
			"-c:v", "h264_amf",
			"-usage", "lowlatency",
			"-quality", "balanced",
			"-rc", "cbr",
		}
	default: // "x264" / ""
		x264Preset := cfg.X264Preset
		if x264Preset == "" {
			x264Preset = "superfast"
		}
		return []string{
			"-vf", "format=bgra,format=yuv420p",
			"-c:v", "libx264",
			"-threads", "2",
			"-preset", x264Preset,
			"-tune", "zerolatency",
		}
	}
}

// x264Preset returns the configured x264 preset, defaulting to "superfast".
func x264Preset(cfg FFmpegConfig) string {
	if cfg.X264Preset != "" {
		return cfg.X264Preset
	}
	return "superfast"
}

// ProbeEncoder picks a hardware H.264 encoder when one actually works on this
// machine, falling back to software x264. Override with SCREEN_SERVER_ENCODER
// (values: auto, nvenc, qsv, amf, x264). Probing once at startup avoids a
// per-session ffmpeg invocation; the test encode validates the encoder and GPU.
func ProbeEncoder(binary string) string {
	if binary == "" {
		binary = "ffmpeg"
	}
	env := strings.ToLower(strings.TrimSpace(os.Getenv("SCREEN_SERVER_ENCODER")))
	switch env {
	case "x264":
		return "x264"
	case "nvenc", "qsv", "amf":
		if encoderWorks(binary, env) {
			return env
		}
		log.Printf("requested encoder %s unavailable; probing others", env)
	case "", "auto":
		// fall through to auto probe
	default:
		log.Printf("unknown SCREEN_SERVER_ENCODER=%q; probing", env)
	}
	for _, e := range []string{"nvenc", "qsv", "amf"} {
		if encoderWorks(binary, e) {
			return e
		}
	}
	return "x264"
}

func encoderWorks(binary, enc string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Minimal test encode: a 320×240 yuv420p frame from testsrc2.
	// Per-encoder baseline args avoid crashes from unsupported combos.
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=1",
		"-frames:v", "1",
		"-pix_fmt", "yuv420p",
	}
	switch enc {
	case "nvenc":
		args = append(args, "-c:v", "h264_nvenc", "-preset", "p1")
	case "qsv":
		args = append(args, "-c:v", "h264_qsv", "-preset", "veryfast")
	case "amf":
		args = append(args, "-c:v", "h264_amf", "-usage", "ultralowlatency")
	}
	args = append(args, "-f", "null", "-")

	cmd := exec.CommandContext(ctx, binary, args...)
	if err := cmd.Run(); err != nil {
		log.Printf("encoder probe %s failed: %v", enc, err)
		return false
	}
	return true
}

func logFFmpeg(stderr io.Reader) {
	// ffmpeg outputs progress lines terminated with \r (carriage return) to
	// overwrite the terminal line, even when stderr is a pipe on some builds.
	// bufio.Scanner would treat the entire run as one enormous line and never
	// emit anything after the initial startup messages. Split manually on both
	// \n and \r so every status line (including speed=) is captured.
	buf := make([]byte, 64*1024)
	var leftover []byte
	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			data := append(leftover, buf[:n]...)
			leftover = nil
			for {
				idx := bytes.IndexAny(data, "\r\n")
				if idx < 0 {
					leftover = append(leftover, data...)
					break
				}
				line := data[:idx]
				// Skip \r\n pair as a single delimiter.
				skip := 1
				if idx+1 < len(data) && data[idx] == '\r' && data[idx+1] == '\n' {
					skip = 2
				}
				data = data[idx+skip:]
				s := strings.TrimSpace(string(line))
				if s == "" {
					continue
				}
				if strings.Contains(s, "speed=") {
					log.Printf("[ffmpeg-progress] %s", s)
				} else {
					log.Printf("ffmpeg: %s", s)
				}
			}
		}
		if err != nil {
			break
		}
	}
	// Flush any trailing data.
	if len(leftover) > 0 {
		s := strings.TrimSpace(string(leftover))
		if s != "" {
			log.Printf("ffmpeg: %s", s)
		}
	}
}

func feedScreencapture(ctx context.Context, stdin io.WriteCloser, cfg FFmpegConfig) {
	defer func() {
		_ = stdin.Close()
	}()

	interval := time.Second / time.Duration(cfg.FPS)
	if interval < 33*time.Millisecond {
		// `screencapture` is a compatibility path, not a 60fps capture API.
		// Keep it bounded so it updates reliably without pinning the machine.
		interval = 33 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("screen-server-%d.jpg", os.Getpid()))
	defer os.Remove(tmp)

	for {
		if err := writeScreencaptureFrame(ctx, stdin, cfg.Display, tmp); err != nil {
			if ctx.Err() == nil {
				log.Printf("screencapture frame failed: %v", err)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func writeScreencaptureFrame(ctx context.Context, stdin io.Writer, display int, tmp string) error {
	args := []string{"-x", "-t", "jpg"}
	if display > 0 {
		args = append(args, "-D", fmt.Sprintf("%d", display))
	}
	args = append(args, tmp)
	cmd := exec.CommandContext(ctx, "screencapture", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("screencapture: %w: %s", err, strings.TrimSpace(string(out)))
	}
	frame, err := os.ReadFile(tmp)
	if err != nil {
		return fmt.Errorf("read screencapture frame: %w", err)
	}
	if len(frame) == 0 {
		return fmt.Errorf("empty screencapture frame")
	}
	if _, err := stdin.Write(frame); err != nil {
		return fmt.Errorf("write frame to ffmpeg: %w", err)
	}
	return nil
}

func displayFromDevice(device string) int {
	if strings.HasPrefix(device, "3") {
		return 2
	}
	return 1
}
