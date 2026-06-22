package capture

import "time"

const (
	NALUTypeIDR = 5
	NALUTypeSPS = 7
	NALUTypePPS = 8
	NALUTypeAUD = 9
)

// EncodedFrame is one H.264 access unit in Annex-B format.
// Data includes start codes and may contain AUD/SPS/PPS/IDR/non-IDR NALUs.
// Step 3 can pass Data directly to Pion's H264 payloader via media.Sample.
type EncodedFrame struct {
	Data       []byte
	Duration   time.Duration
	IsKeyframe bool
	NALUTypes  []byte
}
