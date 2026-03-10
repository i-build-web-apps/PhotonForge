package engine

import (
	"math"
	"sync"

	"gocv.io/x/gocv"
)

// Stacker is the core "Forge" — it aligns incoming frames and accumulates them
// into a 32-bit float sum. A background worker goroutine handles the CPU-heavy
// alignment so the capture loop never blocks.
type Stacker struct {
	Aligner *Aligner

	mu          sync.RWMutex
	accumulator gocv.Mat
	frameCount  int
	initialized bool

	// Latest aligned frame for display (pre-stretch).
	latestAligned gocv.Mat
	latestMatches int

	// Worker channel — frames are sent here for async alignment + stacking.
	work chan gocv.Mat
	done chan struct{}
}

// NewStacker creates a Stacker with a background alignment worker.
func NewStacker(debug bool, workers int) *Stacker {
	if workers < 1 {
		workers = 1
	}
	s := &Stacker{
		Aligner: NewAligner(debug),
		work:    make(chan gocv.Mat, workers*2),
		done:    make(chan struct{}),
	}
	// Single worker to preserve frame ordering and avoid accumulator races.
	// The channel buffer lets the capture loop drop frames if alignment is slow.
	go s.worker()
	return s
}

// Submit sends a frame to the alignment worker. The frame is cloned internally
// so the caller can reuse the Mat immediately.
func (s *Stacker) Submit(frame gocv.Mat) {
	clone := gocv.NewMat()
	frame.CopyTo(&clone)

	select {
	case s.work <- clone:
	default:
		// Worker is busy — drop frame to keep the feed smooth.
		clone.Close()
	}
}

// FrameCount returns the number of frames stacked so far.
func (s *Stacker) FrameCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frameCount
}

// LatestMatches returns the number of feature matches from the last alignment.
func (s *Stacker) LatestMatches() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latestMatches
}

// GetDisplay returns the current accumulator stretched to 8-bit for display.
// Caller must Close the returned Mat.
func (s *Stacker) GetDisplay(black, gamma, white float64) gocv.Mat {
	s.mu.RLock()
	defer s.mu.RUnlock()

	display := gocv.NewMat()
	if !s.initialized || s.accumulator.Empty() {
		return display
	}

	// Normalize: subtract black level, scale to white point, convert to 8-bit.
	normalized := gocv.NewMat()
	defer normalized.Close()

	// Scale factor: maps [black..white] → [0..255]
	scale := 255.0 / (white - black)
	offset := -black * scale

	s.accumulator.ConvertTo(&normalized, gocv.MatTypeCV8U, scale, offset)

	// Apply gamma correction via lookup table.
	if gamma != 1.0 {
		lut := buildGammaLUT(gamma)
		defer lut.Close()
		gocv.LUT(normalized, lut, &display)
	} else {
		normalized.CopyTo(&display)
	}

	return display
}

// GetRawPreview returns the latest aligned frame (not accumulated) for preview.
// Caller must Close the returned Mat.
func (s *Stacker) GetRawPreview() gocv.Mat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := gocv.NewMat()
	if s.latestAligned.Empty() {
		return result
	}
	s.latestAligned.CopyTo(&result)
	return result
}

// Reset clears the accumulator and alignment reference for a new stack.
func (s *Stacker) Reset() {
	s.mu.Lock()
	if s.initialized {
		s.accumulator.Close()
		s.latestAligned.Close()
	}
	s.accumulator = gocv.NewMat()
	s.latestAligned = gocv.NewMat()
	s.frameCount = 0
	s.initialized = false
	s.mu.Unlock()

	s.Aligner.Reset()
}

// Close stops the worker and releases all resources.
func (s *Stacker) Close() {
	close(s.work)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initialized {
		s.accumulator.Close()
		s.latestAligned.Close()
	}
	s.Aligner.Close()
}

func (s *Stacker) worker() {
	defer close(s.done)
	for frame := range s.work {
		s.processFrame(frame)
		frame.Close()
	}
}

func (s *Stacker) processFrame(frame gocv.Mat) {
	aligned, matches, _ := s.Aligner.Align(frame)

	// Convert aligned frame to 32-bit float for accumulation.
	floatFrame := gocv.NewMat()
	defer floatFrame.Close()
	aligned.ConvertTo(&floatFrame, gocv.MatTypeCV32F)

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		s.accumulator = gocv.NewMat()
		floatFrame.CopyTo(&s.accumulator)
		s.latestAligned = gocv.NewMat()
		aligned.CopyTo(&s.latestAligned)
		s.initialized = true
		s.frameCount = 1
		s.latestMatches = matches
		aligned.Close()
		return
	}

	// Additive summation: Accumulator += CurrentFrame
	gocv.Add(s.accumulator, floatFrame, &s.accumulator)
	s.frameCount++
	s.latestMatches = matches

	// Keep latest aligned frame for preview.
	s.latestAligned.Close()
	s.latestAligned = gocv.NewMat()
	aligned.CopyTo(&s.latestAligned)
	aligned.Close()
}

// buildGammaLUT creates a 256-entry lookup table for gamma correction.
func buildGammaLUT(gamma float64) gocv.Mat {
	lut := gocv.NewMatWithSize(1, 256, gocv.MatTypeCV8U)
	invGamma := 1.0 / gamma
	for i := 0; i < 256; i++ {
		val := math.Pow(float64(i)/255.0, invGamma) * 255.0
		if val > 255 {
			val = 255
		}
		lut.SetUCharAt(0, i, uint8(val))
	}
	return lut
}
