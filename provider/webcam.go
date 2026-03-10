package provider

import (
	"fmt"

	"gocv.io/x/gocv"
)

// WebcamProvider captures frames from a connected camera (e.g. Logitech C310).
type WebcamProvider struct {
	DeviceID int
	capture  *gocv.VideoCapture
}

func NewWebcamProvider(deviceID int) *WebcamProvider {
	return &WebcamProvider{DeviceID: deviceID}
}

func (w *WebcamProvider) Open() error {
	cap, err := gocv.OpenVideoCapture(w.DeviceID)
	if err != nil {
		return fmt.Errorf("webcam: failed to open device %d: %w", w.DeviceID, err)
	}
	if !cap.IsOpened() {
		return fmt.Errorf("webcam: device %d is not available", w.DeviceID)
	}
	w.capture = cap
	return nil
}

func (w *WebcamProvider) Read(dst *gocv.Mat) bool {
	if w.capture == nil {
		return false
	}
	return w.capture.Read(dst)
}

func (w *WebcamProvider) Close() error {
	if w.capture != nil {
		return w.capture.Close()
	}
	return nil
}
