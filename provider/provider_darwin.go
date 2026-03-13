//go:build darwin

package provider

// DefaultListVideoDevices returns available cameras using native AVFoundation on macOS.
func DefaultListVideoDevices() []VideoDevice {
	return ListNativeDevices()
}

// DefaultWebcamProvider creates a native AVFoundation webcam provider on macOS.
func DefaultWebcamProvider(device string, width, height, fps int) ImageProvider {
	// device may be a UID (from native listing) or an index (from CLI flags).
	// Native provider requires a UID, so if we got an index like "0",
	// look up the UID from the device list.
	uid := device
	if len(device) <= 2 { // looks like an index, not a UID
		devices := ListNativeDevices()
		for _, d := range devices {
			if d.Index == device {
				uid = d.UID
				break
			}
		}
	}
	return NewNativeWebcamProvider(uid, width, height, fps)
}
