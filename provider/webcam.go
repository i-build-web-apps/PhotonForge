package provider

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// WebcamProvider captures frames from a camera via ffmpeg.
// Works on macOS (avfoundation), Windows (dshow), and Linux (v4l2).
type WebcamProvider struct {
	DeviceID string // "0" on Mac/Linux, device name on Windows
	Width    int
	Height   int
	FPS      int

	cmd    *exec.Cmd
	stdout io.ReadCloser
	buf    []byte // reusable read buffer for one raw frame
}

func NewWebcamProvider(deviceID string, width, height, fps int) *WebcamProvider {
	return &WebcamProvider{
		DeviceID: deviceID,
		Width:    width,
		Height:   height,
		FPS:      fps,
	}
}

func (w *WebcamProvider) Open() error {
	args := w.buildFFmpegArgs()
	w.cmd = exec.Command("ffmpeg", args...)

	var err error
	w.stdout, err = w.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("webcam: stdout pipe: %w", err)
	}

	if err := w.cmd.Start(); err != nil {
		return fmt.Errorf("webcam: failed to start ffmpeg: %w (is ffmpeg installed?)", err)
	}

	// Each frame is Width * Height * 3 bytes (RGB24).
	w.buf = make([]byte, w.Width*w.Height*3)
	return nil
}

func (w *WebcamProvider) Read() *image.NRGBA {
	if w.stdout == nil {
		return nil
	}

	// Read exactly one frame of raw RGB24 data.
	if _, err := io.ReadFull(w.stdout, w.buf); err != nil {
		return nil
	}

	return rgb24ToNRGBA(w.buf, w.Width, w.Height)
}

func (w *WebcamProvider) Close() error {
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
		w.cmd.Wait()
	}
	return nil
}

// ListDevices prints available cameras using ffmpeg.
func ListDevices() (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("ffmpeg", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	case "windows":
		cmd = exec.Command("ffmpeg", "-f", "dshow", "-list_devices", "true", "-i", "dummy")
	default: // linux
		cmd = exec.Command("v4l2-ctl", "--list-devices")
	}
	out, err := cmd.CombinedOutput()
	// ffmpeg returns exit code 1 even on success for list_devices.
	if err != nil && !strings.Contains(string(out), "AVFoundation") && !strings.Contains(string(out), "DirectShow") {
		return "", fmt.Errorf("list devices: %w", err)
	}
	return string(out), nil
}

func (w *WebcamProvider) buildFFmpegArgs() []string {
	size := fmt.Sprintf("%dx%d", w.Width, w.Height)
	fps := fmt.Sprintf("%d", w.FPS)

	switch runtime.GOOS {
	case "darwin":
		return []string{
			"-f", "avfoundation",
			"-framerate", fps,
			"-video_size", size,
			"-i", w.DeviceID,
			"-pix_fmt", "rgb24",
			"-f", "rawvideo",
			"-v", "error",
			"pipe:1",
		}
	case "windows":
		return []string{
			"-f", "dshow",
			"-framerate", fps,
			"-video_size", size,
			"-i", fmt.Sprintf("video=%s", w.DeviceID),
			"-pix_fmt", "rgb24",
			"-f", "rawvideo",
			"-v", "error",
			"pipe:1",
		}
	default: // linux
		dev := w.DeviceID
		if !strings.HasPrefix(dev, "/dev/") {
			dev = "/dev/video" + dev
		}
		return []string{
			"-f", "v4l2",
			"-framerate", fps,
			"-video_size", size,
			"-i", dev,
			"-pix_fmt", "rgb24",
			"-f", "rawvideo",
			"-v", "error",
			"pipe:1",
		}
	}
}

// rgb24ToNRGBA converts a raw RGB24 byte buffer to image.NRGBA.
func rgb24ToNRGBA(data []byte, width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := (y*width + x) * 3
			_ = binary.LittleEndian // hint: ensure bounds check elimination
			img.SetNRGBA(x, y, color.NRGBA{
				R: data[i],
				G: data[i+1],
				B: data[i+2],
				A: 255,
			})
		}
	}
	return img
}
