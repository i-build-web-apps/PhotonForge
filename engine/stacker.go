package engine

import (
	"image"
	"sync"
)

// Stacker is the core "Forge" — it aligns incoming frames and accumulates them
// into a float64 sum. A background worker goroutine handles the CPU-heavy
// alignment so the capture loop never blocks.
type Stacker struct {
	Aligner *Aligner

	mu          sync.RWMutex
	accumulator *FloatImage
	frameCount  int

	latestAligned *image.NRGBA
	latestMatches int

	work chan *image.NRGBA
	done chan struct{}
}

// NewStacker creates a Stacker with a background alignment worker.
func NewStacker(debug bool, bufferSize int) *Stacker {
	if bufferSize < 1 {
		bufferSize = 2
	}
	s := &Stacker{
		Aligner: NewAligner(debug),
		work:    make(chan *image.NRGBA, bufferSize),
		done:    make(chan struct{}),
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

func (s *Stacker) LatestMatches() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latestMatches
}

// GetDisplay returns the current accumulator stretched to 8-bit for display.
func (s *Stacker) GetDisplay(black, gamma, white float64) *image.NRGBA {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.accumulator == nil {
		return nil
	}
	return s.accumulator.Stretch(black, gamma, white)
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
func (s *Stacker) Reset() {
	s.mu.Lock()
	s.accumulator = nil
	s.latestAligned = nil
	s.frameCount = 0
	s.latestMatches = 0
	s.mu.Unlock()

	s.Aligner.Reset()
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
	result := s.Aligner.Align(frame)
	floatFrame := FromNRGBA(result.Image)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.accumulator == nil {
		s.accumulator = floatFrame
		s.frameCount = 1
	} else {
		s.accumulator.Add(floatFrame)
		s.frameCount++
	}

	s.latestAligned = result.Image
	s.latestMatches = result.Matches
}
