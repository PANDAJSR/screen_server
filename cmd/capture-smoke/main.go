package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"time"

	"screen_server/internal/capture"
)

func main() {
	seconds := flag.Int("seconds", 3, "capture duration")
	device := flag.String("device", "", "ffmpeg input device, e.g. macOS avfoundation 2:none")
	flag.Parse()

	cfg := capture.DefaultFFmpegConfig()
	if *device != "" {
		cfg.Device = *device
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*seconds)*time.Second)
	defer cancel()

	stream, err := capture.StartFFmpegCapture(ctx, cfg)
	if err != nil {
		log.Fatalf("start capture: %v", err)
	}
	defer func() {
		if err := stream.Stop(); err != nil {
			log.Printf("stop capture: %v", err)
		}
	}()

	reader := stream.Reader()
	var frames int
	var keyframes int
	for ctx.Err() == nil {
		frame, err := reader.ReadFrame()
		if err != nil {
			if errors.Is(err, capture.ErrClosed) {
				break
			}
			log.Fatalf("read h264 frame: %v", err)
		}
		frames++
		if frame.IsKeyframe {
			keyframes++
		}
		log.Printf("frame=%d bytes=%d keyframe=%v nalus=%v duration=%s", frames, len(frame.Data), frame.IsKeyframe, frame.NALUTypes, frame.Duration)
	}
	log.Printf("captured frames=%d keyframes=%d", frames, keyframes)
}
