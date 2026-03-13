#ifndef WEBCAM_DARWIN_H
#define WEBCAM_DARWIN_H

#include <stdint.h>
#include <stddef.h>

typedef struct CameraHandle CameraHandle;

// List devices; caller frees returned arrays with free_device_list()
int list_video_devices(char*** names, char*** uids, int* count);
void free_device_list(char** names, char** uids, int count);

// Open a device by uniqueID; returns opaque handle or NULL
CameraHandle* camera_open(const char* uid, int width, int height, int fps);

// Read next frame into caller-supplied buffer (BGRA, width*height*4 bytes)
// Returns 0 on success, -1 on timeout/error
int camera_read_frame(CameraHandle* cam, uint8_t* buf, size_t buf_len);

// Signal the grabber to abort any pending read (unblocks camera_read_frame).
// Safe to call from any thread before camera_close.
void camera_signal_close(CameraHandle* cam);

// Query the actual resolution delivered by the camera (may differ from requested).
// Returns 0 on success, -1 if no frames received yet.
int camera_actual_size(CameraHandle* cam, int* out_width, int* out_height);

// Close and release session
void camera_close(CameraHandle** cam);

#endif
