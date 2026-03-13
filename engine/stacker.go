package engine

import (
	"image"
	"math/rand"
	"sort"
	"sync"
)

// StackMode determines how frames are combined in the accumulator.
type StackMode int

const (
	StackAverage   StackMode = iota // Sum frames, divide by N at display (default)
	StackMaximum                    // Per-pixel brightest value
	StackMedian                     // Per-pixel median (robust outlier rejection)
	StackSigmaClip                  // Reject outliers beyond 2σ, then average
)

func (m StackMode) String() string {
	switch m {
	case StackAverage:
		return "Average"
	case StackMaximum:
		return "Maximum"
	case StackMedian:
		return "Median"
	case StackSigmaClip:
		return "Sigma Clip"
	default:
		return "Unknown"
	}
}

// StretchMode determines how the accumulator is rendered for display.
type StretchMode int

const (
	StretchLinear  StretchMode = iota // Black/Gamma/White (default)
	StretchAutoSTF                    // Automatic Screen Transfer Function
	StretchAsinh                      // Arcsinh stretch for faint detail
)

func (m StretchMode) String() string {
	switch m {
	case StretchLinear:
		return "Linear"
	case StretchAutoSTF:
		return "Auto STF"
	case StretchAsinh:
		return "Asinh"
	default:
		return "Unknown"
	}
}

// Stacker is the core "Forge" — it aligns incoming frames and accumulates them
// into a float64 sum using a rolling window. A background worker goroutine
// handles the CPU-heavy alignment so the capture loop never blocks.
type Stacker struct {
	Aligner *Aligner

	mu          sync.RWMutex
	accumulator *FloatImage
	frameCount  int // total frames processed (lifetime)

	latestAligned  *image.NRGBA
	latestMatches  int
	latestDetected int
	droppedFrames  int // frames skipped due to poor alignment

	minMatchThreshold int // minimum star matches to accept a frame (default 3)

	// Stack mode — determines how frames are combined.
	stackMode    StackMode
	sigmaClipVal float64 // sigma threshold for StackSigmaClip (default 2.0)

	// Stretch/display mode — determines how the accumulator is rendered.
	stretchMode StretchMode
	asinhBeta   float64 // intensity for Asinh stretch (default 10.0)
	stfParams   STFParams
	stfDirty    bool // true when accumulator changed and STF needs recomputing

	// Rolling stack — keeps the last stackDepth aligned frames in a ring
	// buffer so old frames can be subtracted from the accumulator.
	stackDepth int           // 0 = unlimited (legacy sum-forever mode)
	ring       []*FloatImage // circular buffer of aligned float frames
	ringPos    int           // next write position
	ringLen    int           // frames currently in the ring

	// Reset epoch — incremented on Reset/SetImage so in-flight frames are discarded.
	epoch int

	work chan *image.NRGBA
	done chan struct{}
}

// NewStacker creates a Stacker with a background alignment worker.
// Default stack depth is 30 frames (rolling).
func NewStacker(debug bool, bufferSize int) *Stacker {
	if bufferSize < 1 {
		bufferSize = 2
	}
	s := &Stacker{
		Aligner:           NewAligner(debug),
		stackDepth:        30,
		minMatchThreshold: 3,
		stackMode:         StackAverage,
		sigmaClipVal:      2.0,
		stretchMode:       StretchLinear,
		asinhBeta:         10.0,
		stfDirty:          true,
		work:              make(chan *image.NRGBA, bufferSize),
		done:              make(chan struct{}),
	}
	go s.worker()
	return s
}

// Submit sends a frame to the alignment worker. Non-blocking — drops the frame
// if the worker is busy, keeping the feed smooth.
func (s *Stacker) Submit(frame *image.NRGBA) {
	clone := cloneImage(frame)
	select {
	case s.work <- clone:
	default:
		// Worker is busy — drop frame.
	}
}

func (s *Stacker) FrameCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frameCount
}

// StackedFrames returns the number of frames currently contributing to the
// displayed stack. In rolling mode this is the ring occupancy; in unlimited
// mode it equals FrameCount.
func (s *Stacker) StackedFrames() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.stackDepth > 0 {
		return s.ringLen
	}
	return s.frameCount
}

func (s *Stacker) LatestMatches() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latestMatches
}

func (s *Stacker) LatestDetected() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latestDetected
}

// DroppedFrames returns the number of frames skipped due to poor alignment.
func (s *Stacker) DroppedFrames() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.droppedFrames
}

// SetMinMatchThreshold sets the minimum star matches required to accept a frame.
func (s *Stacker) SetMinMatchThreshold(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 {
		n = 1
	}
	s.minMatchThreshold = n
}

// SetMaxStars updates the max stars used for detection in the aligner.
func (s *Stacker) SetMaxStars(n int) {
	s.Aligner.SetMaxStars(n)
}

// LatestStars returns a copy of the star list detected in the most recent frame.
func (s *Stacker) LatestStars() []Star {
	stars := s.Aligner.LatestStars()
	if stars == nil {
		return nil
	}
	out := make([]Star, len(stars))
	copy(out, stars)
	return out
}

func (s *Stacker) StackDepth() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stackDepth
}

func (s *Stacker) StackMode() StackMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stackMode
}

func (s *Stacker) SetStackMode(mode StackMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode == s.stackMode {
		return
	}
	s.stackMode = mode
	// Recompute accumulator from ring buffer for the new mode.
	s.recomputeAccumulator()
}

func (s *Stacker) SigmaClipVal() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sigmaClipVal
}

func (s *Stacker) StretchMode() StretchMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stretchMode
}

func (s *Stacker) SetStretchMode(mode StretchMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stretchMode = mode
	if mode == StretchAutoSTF && s.accumulator != nil {
		s.stfParams = s.accumulator.ComputeSTFParams(s.displayFrameScale())
	}
}

func (s *Stacker) AsinhBeta() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.asinhBeta
}

func (s *Stacker) SetAsinhBeta(v float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v < 1 {
		v = 1
	}
	if v > 500 {
		v = 500
	}
	s.asinhBeta = v
}

func (s *Stacker) SetSigmaClipVal(v float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v < 0.5 {
		v = 0.5
	}
	if v > 5.0 {
		v = 5.0
	}
	s.sigmaClipVal = v
	if s.stackMode == StackSigmaClip {
		s.recomputeAccumulator()
	}
}

// SetStackDepth changes the rolling window size. 0 = unlimited.
// When shrinking, the accumulator is rebuilt from the most recent frames.
func (s *Stacker) SetStackDepth(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if n == s.stackDepth {
		return
	}

	// Collect existing frames from old ring in chronological order.
	var oldFrames []*FloatImage
	if s.stackDepth > 0 && s.ringLen > 0 {
		for i := 0; i < s.ringLen; i++ {
			idx := (s.ringPos - s.ringLen + i + len(s.ring)) % len(s.ring)
			if s.ring[idx] != nil {
				oldFrames = append(oldFrames, s.ring[idx])
			}
		}
	}

	s.stackDepth = n

	if n == 0 {
		// Switching to unlimited — keep current accumulator, discard ring.
		s.ring = nil
		s.ringPos = 0
		s.ringLen = 0
		return
	}

	// Create new ring and rebuild accumulator from available frames.
	s.ring = make([]*FloatImage, n)
	s.ringPos = 0
	s.ringLen = 0
	s.accumulator = nil

	if len(oldFrames) > 0 {
		// Keep the most recent frames that fit.
		start := 0
		if len(oldFrames) > n {
			start = len(oldFrames) - n
		}
		for i := start; i < len(oldFrames); i++ {
			s.ring[s.ringPos] = oldFrames[i]
			s.ringPos = (s.ringPos + 1) % n
			s.ringLen++
		}
		s.recomputeAccumulator()
	}
}

// GetDisplay returns the current accumulator stretched to 8-bit for display.
// Stretch parameters (black, gamma, white) operate in per-frame space (0–255),
// and are automatically scaled by the number of stacked frames so the display
// remains correct regardless of stack depth.
func (s *Stacker) GetDisplay(black, gamma, white float64) *image.NRGBA {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.accumulator == nil {
		return nil
	}

	frames := s.displayFrameScale()
	switch s.stretchMode {
	case StretchAutoSTF:
		// stfParams are pre-computed in processFrame under write lock.
		return s.accumulator.StretchAutoSTF(frames, s.stfParams)
	case StretchAsinh:
		return s.accumulator.StretchAsinh(black*frames, s.asinhBeta, white*frames)
	default:
		return s.accumulator.Stretch(black*frames, gamma, white*frames)
	}
}

// GetDisplay16 returns the current accumulator stretched to 16-bit for TIFF export.
func (s *Stacker) GetDisplay16(black, gamma, white float64) *image.NRGBA64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.accumulator == nil {
		return nil
	}

	frames := s.displayFrameScale()
	// 16-bit export always uses linear stretch.
	return s.accumulator.Stretch16(black*frames, gamma, white*frames)
}

// displayFrameScale returns the multiplier for stretch params. When the
// accumulator holds a raw sum, this equals the frame count so that stretch
// params in per-frame (0–255) space map correctly. When the accumulator is
// already normalised (Max/Median/SigmaClip in rolling mode) it returns 1.
// Must be called with s.mu held.
func (s *Stacker) displayFrameScale() float64 {
	if s.stackDepth > 0 && s.stackMode != StackAverage {
		// Rolling + non-Average: accumulator is in per-frame space.
		return 1.0
	}
	// Accumulator is a raw sum — scale by active frame count.
	// This covers: Average (always sum), and unlimited-mode fallback for
	// Median/SigmaClip (which fall back to sum without a ring buffer).
	frames := float64(s.ringLen)
	if s.stackDepth == 0 {
		frames = float64(s.frameCount)
	}
	if frames < 1 {
		frames = 1
	}
	return frames
}

// GetPreview returns the latest aligned frame (not accumulated).
func (s *Stacker) GetPreview() *image.NRGBA {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.latestAligned == nil {
		return nil
	}
	return cloneImage(s.latestAligned)
}

// Reset clears the accumulator and alignment reference for a new stack.
// The stack depth setting is preserved. Drains any buffered frames.
func (s *Stacker) Reset() {
	// Drain any buffered frames from the work channel so they don't
	// get processed after reset.
	for {
		select {
		case <-s.work:
		default:
			goto drained
		}
	}
drained:

	s.mu.Lock()
	s.epoch++
	s.accumulator = nil
	s.latestAligned = nil
	s.frameCount = 0
	s.latestMatches = 0
	s.latestDetected = 0
	s.droppedFrames = 0
	if s.stackDepth > 0 {
		s.ring = make([]*FloatImage, s.stackDepth)
	} else {
		s.ring = nil
	}
	s.ringPos = 0
	s.ringLen = 0
	s.mu.Unlock()

	s.Aligner.Reset()
}

// SetImage directly sets a single image as the stack (bypassing the async worker).
// Used for imported images where no alignment pipeline is needed.
func (s *Stacker) SetImage(img *image.NRGBA) {
	stars := DetectStars(img, 80)

	floatFrame := FromNRGBA(img)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.epoch++
	s.accumulator = floatFrame
	s.latestAligned = img
	s.frameCount = 1
	s.latestDetected = len(stars)
	s.latestMatches = len(stars)
	s.droppedFrames = 0

	if s.stackDepth > 0 {
		s.ring = make([]*FloatImage, s.stackDepth)
		s.ring[0] = floatFrame
		s.ringPos = 1
		s.ringLen = 1
	} else {
		s.ring = nil
		s.ringPos = 0
		s.ringLen = 0
	}

	s.stfDirty = true
	s.Aligner.Reset()
	s.Aligner.latestStars = stars
}

// Close stops the worker and releases all resources.
func (s *Stacker) Close() {
	close(s.work)
	<-s.done
}

func (s *Stacker) worker() {
	defer close(s.done)
	for frame := range s.work {
		s.processFrame(frame)
	}
}

func (s *Stacker) processFrame(frame *image.NRGBA) {
	// Capture epoch before the (unlocked) alignment work.
	s.mu.RLock()
	epochBefore := s.epoch
	s.mu.RUnlock()

	result := s.Aligner.Align(frame)

	s.mu.Lock()
	defer s.mu.Unlock()

	// If Reset/SetImage was called during Align, discard this stale frame.
	if s.epoch != epochBefore {
		return
	}

	s.frameCount++
	s.latestAligned = result.Image
	s.latestMatches = result.Matches
	s.latestDetected = result.DetectedStars

	// Quality gate: skip frames that had enough stars for alignment but
	// failed to match. If the scene has fewer stars than the threshold,
	// let the frame through — there's nothing to match against.
	if s.frameCount > 1 && result.DetectedStars >= s.minMatchThreshold && result.Matches < s.minMatchThreshold {
		s.droppedFrames++
		return
	}

	floatFrame := FromNRGBA(result.Image)

	if s.stackDepth > 0 {
		// Rolling mode — maintain ring buffer.
		if s.ring == nil || len(s.ring) != s.stackDepth {
			s.ring = make([]*FloatImage, s.stackDepth)
			s.ringPos = 0
			s.ringLen = 0
			s.accumulator = nil
		}

		// For Average mode, use fast incremental add/subtract.
		// For other modes, store in ring then recompute from all frames.
		if s.stackMode == StackAverage {
			// Subtract the frame being evicted (if ring is full).
			if s.ringLen == s.stackDepth && s.ring[s.ringPos] != nil {
				s.accumulator.Sub(s.ring[s.ringPos])
			}
			// Add new frame to accumulator.
			if s.accumulator == nil {
				s.accumulator = floatFrame.Clone()
			} else {
				s.accumulator.Add(floatFrame)
			}
			// Store in ring.
			s.ring[s.ringPos] = floatFrame
			if s.ringLen < s.stackDepth {
				s.ringLen++
			}
			s.ringPos = (s.ringPos + 1) % s.stackDepth
		} else {
			// Non-incremental modes — store in ring, then recompute.
			s.ring[s.ringPos] = floatFrame
			if s.ringLen < s.stackDepth {
				s.ringLen++
			}
			s.ringPos = (s.ringPos + 1) % s.stackDepth
			s.recomputeAccumulator()
		}
	} else {
		// Unlimited mode — no ring buffer available.
		switch s.stackMode {
		case StackMaximum:
			// Incremental per-pixel max.
			if s.accumulator == nil {
				s.accumulator = floatFrame.Clone()
			} else {
				for j := range s.accumulator.Pix {
					if floatFrame.Pix[j] > s.accumulator.Pix[j] {
						s.accumulator.Pix[j] = floatFrame.Pix[j]
					}
				}
			}
		default:
			// Average, Median, SigmaClip — all fall back to incremental sum
			// in unlimited mode (Median/SigmaClip need frame history which
			// isn't available without a ring buffer).
			if s.accumulator == nil {
				s.accumulator = floatFrame
			} else {
				s.accumulator.Add(floatFrame)
			}
		}
	}
	// Pre-compute STF params under the write lock so GetDisplay (RLock) never writes.
	if s.stretchMode == StretchAutoSTF && s.accumulator != nil {
		s.stfParams = s.accumulator.ComputeSTFParams(s.displayFrameScale())
	}
	s.stfDirty = true
}

// recomputeAccumulator rebuilds the accumulator from the ring buffer
// using the current stack mode. Must be called with s.mu held.
func (s *Stacker) recomputeAccumulator() {
	frames := s.ringFrames()
	if len(frames) == 0 {
		s.accumulator = nil
		return
	}

	if s.accumulator == nil {
		s.accumulator = NewFloatImage(frames[0].Width, frames[0].Height)
	}

	switch s.stackMode {
	case StackAverage:
		s.accumulator.SumFrom(frames)
	case StackMaximum:
		s.accumulator.MaxFrom(frames)
	case StackMedian:
		s.accumulator.MedianFrom(frames)
	case StackSigmaClip:
		s.accumulator.SigmaClipFrom(frames, s.sigmaClipVal)
	}
	s.stfDirty = true
}

// AccumulatorStats samples the accumulator and returns statistics in per-frame
// space (i.e. divided by frameScale). Returns median, p95, max, and frameScale.
// Returns zeros if no accumulator is present.
func (s *Stacker) AccumulatorStats() (median, p95, maxVal, frameScale float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.accumulator == nil {
		return 0, 0, 0, 1
	}

	frameScale = s.displayFrameScale()
	pix := s.accumulator.Pix
	total := len(pix) / 3 // number of pixels (each has R,G,B at stride 3)

	// Sample up to ~100k luminance values (R channel as proxy).
	const maxSamples = 100_000
	stride := 1
	if total > maxSamples {
		stride = total / maxSamples
	}

	samples := make([]float64, 0, total/stride+1)
	rng := rand.New(rand.NewSource(42))
	_ = rng // deterministic stride sampling below

	for i := 0; i < total; i += stride {
		// Luminance approximation: 0.299R + 0.587G + 0.114B
		base := i * 3
		lum := 0.299*float64(pix[base]) + 0.587*float64(pix[base+1]) + 0.114*float64(pix[base+2])
		// Convert to per-frame space.
		lum /= frameScale
		samples = append(samples, lum)
		if lum > maxVal {
			maxVal = lum
		}
	}

	sort.Float64s(samples)
	n := len(samples)
	if n == 0 {
		return 0, 0, maxVal, frameScale
	}

	median = samples[n/2]
	p95Idx := int(float64(n) * 0.95)
	if p95Idx >= n {
		p95Idx = n - 1
	}
	p95 = samples[p95Idx]
	return median, p95, maxVal, frameScale
}

// ringFrames returns the non-nil frames from the ring in chronological order.
func (s *Stacker) ringFrames() []*FloatImage {
	if s.ringLen == 0 {
		return nil
	}
	frames := make([]*FloatImage, 0, s.ringLen)
	for i := 0; i < s.ringLen; i++ {
		idx := (s.ringPos - s.ringLen + i + len(s.ring)) % len(s.ring)
		if s.ring[idx] != nil {
			frames = append(frames, s.ring[idx])
		}
	}
	return frames
}
