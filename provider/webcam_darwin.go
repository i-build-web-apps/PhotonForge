//go:build darwin

package provider

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework Foundation
#include "webcam_darwin.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"image"
	"sync"
	"unsafe"
)

// NativeWebcamProvider captures frames via AVFoundation on macOS.
// No external dependencies (no ffmpeg).
type NativeWebcamProvider struct {
	DeviceUID string
	Width     int
	Height    int
	FPS       int

	mu          sync.RWMutex
	handle      *C.CameraHandle
	buf         []byte
	actualW     int  // actual resolution delivered by camera
	actualH     int
	sizeChecked bool // whether we've queried actual size
}

func NewNativeWebcamProvider(uid string, width, height, fps int) *NativeWebcamProvider {
	return &NativeWebcamProvider{
		DeviceUID: uid,
		Width:     width,
		Height:    height,
		FPS:       fps,
	}
}

func (p *NativeWebcamProvider) Open() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.handle != nil {
		return nil // already open
	}

	cUID := C.CString(p.DeviceUID)
	defer C.free(unsafe.Pointer(cUID))

	h := C.camera_open(cUID, C.int(p.Width), C.int(p.Height), C.int(p.FPS))
	if h == nil {
		return fmt.Errorf("native webcam: failed to open device %q", p.DeviceUID)
	}
	p.handle = h
	p.buf = make([]byte, p.Width*p.Height*4)
	return nil
}

func (p *NativeWebcamProvider) Read() *image.NRGBA {
	// Hold RLock for the entire read so Close() cannot free the handle
	// while camera_read_frame is in-flight.
	p.mu.RLock()
	defer p.mu.RUnlock()

	h := p.handle
	if h == nil {
		return nil
	}

	ret := C.camera_read_frame(
		h,
		(*C.uint8_t)(unsafe.Pointer(&p.buf[0])),
		C.size_t(len(p.buf)),
	)
	if ret != 0 {
		return nil
	}

	// On first successful read, query the actual delivered resolution.
	// If it differs from requested, adapt our output dimensions.
	if !p.sizeChecked {
		var aw, ah C.int
		if C.camera_actual_size(h, &aw, &ah) == 0 {
			p.actualW = int(aw)
			p.actualH = int(ah)
			if p.actualW != p.Width || p.actualH != p.Height {
				fmt.Printf("webcam: requested %dx%d, camera delivers %dx%d\n",
					p.Width, p.Height, p.actualW, p.actualH)
			}
		}
		p.sizeChecked = true
	}

	// Use the smaller of requested vs actual for the output image.
	outW := p.Width
	outH := p.Height
	if p.actualW > 0 && p.actualW < outW {
		outW = p.actualW
	}
	if p.actualH > 0 && p.actualH < outH {
		outH = p.actualH
	}

	// Convert BGRA → NRGBA (swap B and R channels).
	stride := p.Width * 4 // buffer stride is always based on requested width
	img := image.NewNRGBA(image.Rect(0, 0, outW, outH))
	for y := 0; y < outH; y++ {
		srcOff := y * stride
		dstOff := y * outW * 4
		for x := 0; x < outW; x++ {
			si := srcOff + x*4
			di := dstOff + x*4
			img.Pix[di+0] = p.buf[si+2] // R ← B
			img.Pix[di+1] = p.buf[si+1] // G
			img.Pix[di+2] = p.buf[si+0] // B ← R
			img.Pix[di+3] = 0xFF        // A
		}
	}
	return img
}

func (p *NativeWebcamProvider) Close() error {
	// Signal the grabber to abort any blocked read BEFORE taking the
	// write lock. This unblocks camera_read_frame so Read() releases
	// its RLock, allowing us to acquire the write lock without deadlock.
	p.mu.RLock()
	h := p.handle
	p.mu.RUnlock()
	if h != nil {
		C.camera_signal_close(h)
	}

	// Now take exclusive lock — any in-flight Read() will have returned.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == nil {
		return nil
	}
	C.camera_close(&p.handle)
	p.handle = nil
	return nil
}

// ListNativeDevices returns available video capture devices via AVFoundation.
func ListNativeDevices() []VideoDevice {
	var cNames, cUIDs **C.char
	var count C.int
	C.list_video_devices(&cNames, &cUIDs, &count)
	n := int(count)
	if n == 0 {
		return nil
	}
	defer C.free_device_list(cNames, cUIDs, count)

	names := unsafe.Slice((*unsafe.Pointer)(unsafe.Pointer(cNames)), n)
	uids := unsafe.Slice((*unsafe.Pointer)(unsafe.Pointer(cUIDs)), n)

	devices := make([]VideoDevice, n)
	for i := 0; i < n; i++ {
		devices[i] = VideoDevice{
			Index: fmt.Sprintf("%d", i),
			Name:  C.GoString((*C.char)(names[i])),
			UID:   C.GoString((*C.char)(uids[i])),
		}
	}
	return devices
}
