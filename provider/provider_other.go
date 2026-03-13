//go:build !darwin

package provider

// DefaultListVideoDevices returns available cameras using ffmpeg on non-macOS platforms.
func DefaultListVideoDevices() []VideoDevice {
	return ListVideoDevices()
}

// DefaultWebcamProvider creates an ffmpeg-based webcam provider on non-macOS platforms.
func DefaultWebcamProvider(device string, width, height, fps int) ImageProvider {
	return NewWebcamProvider(device, width, height, fps)
}
