package capture

import (
	"bufio"
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
}

func DefaultFFmpegConfig() FFmpegConfig {
	return FFmpegConfig{
		Binary:      "ffmpeg",
		Device:      defaultScreenDevice(),
		FPS:         60,
		Bitrate:     "8M",
		MaxRate:     "12M",
		BufferSize:  "1M",
		GOP:         15,
		UseHardware: true,
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
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", "baseline",
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	}
}

func buildTestSourceArgs(cfg FFmpegConfig) []string {
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
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", "baseline",
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
	captureCursor := "0"
	if cfg.DrawMouse {
		captureCursor = "1"
	}
	args := []string{
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
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", "baseline",
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	}
	if cfg.UseHardware {
		args = append([]string{
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
			"-b:v", cfg.Bitrate,
			"-maxrate", cfg.MaxRate,
			"-bufsize", cfg.BufferSize,
			"-g", fmt.Sprintf("%d", cfg.GOP),
			"-bf", "0",
			"-profile:v", "baseline",
			"-bsf:v", "h264_metadata=aud=insert",
			"-f", "h264",
			"pipe:1",
		})
	}
	return args
}

func buildX11Args(cfg FFmpegConfig) []string {
	drawMouse := "0"
	if cfg.DrawMouse {
		drawMouse = "1"
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
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", "baseline",
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
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer",
		"-f", "gdigrab",
		"-draw_mouse", drawMouse,
		"-framerate", fmt.Sprintf("%d", cfg.FPS),
		"-i", "desktop",
		"-an",
		"-vf", "format=yuv420p",
		"-c:v", "libx264",
		"-threads", "2",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-b:v", cfg.Bitrate,
		"-maxrate", cfg.MaxRate,
		"-bufsize", cfg.BufferSize,
		"-g", fmt.Sprintf("%d", cfg.GOP),
		"-bf", "0",
		"-profile:v", "baseline",
		"-avioflags", "direct",
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264",
		"pipe:1",
	}
}

func logFFmpeg(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			log.Printf("ffmpeg: %s", line)
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
