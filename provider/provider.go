package provider

import "image"

// ImageProvider abstracts the source of frames — webcam or test images.
type ImageProvider interface {
	// Open initialises the provider and prepares it to deliver frames.
	Open() error
	// Read returns the next frame, or nil when no more frames are available.
	Read() *image.NRGBA
	// Close releases all resources held by the provider.
	Close() error
}
