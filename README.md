# Screen Server

Extremely low-latency desktop screen sharing over WebRTC.

## Step 1 Architecture

Current scope:

- Go HTTP server on `:8080`
- WebSocket signaling endpoint: `/api/signaling/ws?room=default`
- React 18 + TypeScript frontend created with Vite
- JSON signaling envelope for future SDP and ICE exchange

Signaling message shape:

```json
{
  "type": "offer | answer | candidate | ping | pong",
  "room": "default",
  "from": "sender-client-id",
  "to": "optional-target-client-id",
  "payload": {}
}
```

The server currently works as a room relay. Step 3 will attach the Go Pion
peer to this signaling flow and consume `offer` / `candidate` messages.

## Development

Backend:

```bash
npm run dev:backend
```

Backend variants:

```bash
npm run dev:backend:testsrc
npm run dev:backend:screencap
npm run dev:backend:screen0
npm run dev:backend:screen1
```

Frontend:

```bash
npm run dev:frontend
```

Validation:

```bash
npm run test:backend
npm run build:frontend
```

Capture smoke test:

```bash
npm run capture:smoke -- -seconds 3
```

To isolate WebRTC from macOS screen capture, run the backend with a synthetic
moving source:

```bash
SCREEN_SERVER_CAPTURE_INPUT=testsrc npm run dev:backend
```

On this macOS machine the browser is on FFmpeg input `3:none`
(`Capture screen 1`). The other display is `2:none` (`Capture screen 0`).
Use `ffmpeg -f avfoundation -list_devices true -i ""` if the indexes change.

Step 2 capture path:

- `internal/capture/ffmpeg.go` starts FFmpeg through `os/exec`.
- macOS uses `avfoundation` + `h264_videotoolbox`.
- Cursor capture is disabled with `-capture_cursor 0`.
- Output is raw H.264 Annex-B on stdout.
- `internal/capture/annexb.go` groups NALUs into frame-sized access units using
  inserted AUD NALUs, which is the shape Pion needs for `media.Sample`.

Step 3/4 WebRTC path:

- The React viewer creates a recvonly video transceiver and sends an SDP offer
  over WebSocket.
- `internal/rtc/manager.go` creates a Pion PeerConnection, answers with H.264,
  forwards ICE candidates, and writes FFmpeg Annex-B frames into
  `TrackLocalStaticSample`.
- RTCP PLI/FIR keyframe requests are logged. The current FFmpeg process path
  relies on a short GOP for recovery; a lower-level VideoToolbox integration can
  map those callbacks to force-keyframe later.
- Backpressure policy is low-latency: each session keeps only one queued encoded
  frame and drops stale frames instead of allowing network stalls to block
  capture.

Open the Vite URL, usually `http://localhost:5173`.
