package provider

import "gocv.io/x/gocv"

// ImageProvider abstracts the source of frames — webcam or test images.
type ImageProvider interface {
	// Open initialises the provider and prepares it to deliver frames.
	Open() error
	// Read returns the next frame. Returns false when no more frames are available.
	Read(dst *gocv.Mat) bool
	// Close releases all resources held by the provider.
	Close() error
}
