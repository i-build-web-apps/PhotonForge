#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#include "webcam_darwin.h"
#include <dispatch/dispatch.h>
#include <string.h>
#include <stdlib.h>

// ---------------------------------------------------------------------------
// FrameGrabber — AVCaptureVideoDataOutputSampleBufferDelegate
// Double-buffered: capture thread writes, Go thread reads.
// ---------------------------------------------------------------------------

@interface FrameGrabber : NSObject <AVCaptureVideoDataOutputSampleBufferDelegate> {
    uint8_t* _frameBuf[2];
}
@property (nonatomic) int      writeSlot;
@property (nonatomic) int      readSlot;
@property (nonatomic) int      frameWidth;
@property (nonatomic) int      frameHeight;
@property (nonatomic) int      actualWidth;   // delivered by camera
@property (nonatomic) int      actualHeight;  // delivered by camera
@property (nonatomic) BOOL     closed;
@property (nonatomic, strong) dispatch_semaphore_t frameSem;
@property (nonatomic, strong) NSLock*  lock;
@end

@implementation FrameGrabber

- (instancetype)initWithWidth:(int)w height:(int)h {
    self = [super init];
    if (self) {
        _frameWidth  = w;
        _frameHeight = h;
        _frameSem    = dispatch_semaphore_create(0);
        _lock        = [[NSLock alloc] init];
        _writeSlot   = 0;
        _readSlot    = -1;
        size_t sz = w * h * 4;
        for (int i = 0; i < 2; i++) {
            _frameBuf[i] = (uint8_t*)malloc(sz);
        }
    }
    return self;
}

#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wobjc-missing-super-calls"
- (void)dealloc {
    for (int i = 0; i < 2; i++) free(_frameBuf[i]);
}
#pragma clang diagnostic pop

- (void)captureOutput:(AVCaptureOutput *)output
didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
       fromConnection:(AVCaptureConnection *)connection
{
    if (_closed) return;

    CVImageBufferRef pixelBuf = CMSampleBufferGetImageBuffer(sampleBuffer);
    if (!pixelBuf) return;

    CVPixelBufferLockBaseAddress(pixelBuf, kCVPixelBufferLock_ReadOnly);

    size_t width  = CVPixelBufferGetWidth(pixelBuf);
    size_t height = CVPixelBufferGetHeight(pixelBuf);
    uint8_t* src  = (uint8_t*)CVPixelBufferGetBaseAddress(pixelBuf);
    size_t srcRowBytes = CVPixelBufferGetBytesPerRow(pixelBuf);

    if (!src) {
        CVPixelBufferUnlockBaseAddress(pixelBuf, kCVPixelBufferLock_ReadOnly);
        return;
    }

    [_lock lock];
    // Track actual delivered resolution (may differ from requested).
    _actualWidth  = (int)width;
    _actualHeight = (int)height;
    int slot = _writeSlot;
    uint8_t* dst = _frameBuf[slot];
    size_t dstRowBytes = (size_t)_frameWidth * 4;
    size_t totalBytes  = dstRowBytes * (size_t)_frameHeight;

    // Zero the entire buffer so unfilled regions (camera delivers fewer
    // rows/cols than requested) show as black instead of stale data.
    memset(dst, 0, totalBytes);

    // Clamp to our allocated buffer dimensions — the camera may deliver
    // a different resolution than requested.
    size_t copyW = width  < (size_t)_frameWidth  ? width  : (size_t)_frameWidth;
    size_t copyH = height < (size_t)_frameHeight ? height : (size_t)_frameHeight;
    size_t copyBytes   = copyW * 4;
    for (size_t row = 0; row < copyH; row++) {
        memcpy(dst + row * dstRowBytes, src + row * srcRowBytes, copyBytes);
    }
    _readSlot  = slot;
    _writeSlot = (slot + 1) % 2;
    [_lock unlock];

    CVPixelBufferUnlockBaseAddress(pixelBuf, kCVPixelBufferLock_ReadOnly);

    dispatch_semaphore_signal(_frameSem);
}

- (int)readFrameInto:(uint8_t*)buf length:(size_t)len timeout_ms:(int)ms {
    if (_closed) return -1;

    dispatch_time_t t = dispatch_time(DISPATCH_TIME_NOW,
                                      (int64_t)ms * NSEC_PER_MSEC);
    if (dispatch_semaphore_wait(_frameSem, t) != 0) return -1;
    if (_closed) return -1;

    [_lock lock];
    int slot = _readSlot;
    if (slot < 0) { [_lock unlock]; return -1; }
    size_t sz = (size_t)_frameWidth * (size_t)_frameHeight * 4;
    if (len < sz) sz = len;
    memcpy(buf, _frameBuf[slot], sz);
    [_lock unlock];
    return 0;
}

- (void)signalClose {
    _closed = YES;
    // Unblock any thread waiting in readFrameInto.
    dispatch_semaphore_signal(_frameSem);
}

@end

// ---------------------------------------------------------------------------
// Opaque handle
// ---------------------------------------------------------------------------

struct CameraHandle {
    AVCaptureSession*          session;
    AVCaptureDeviceInput*      input;
    AVCaptureVideoDataOutput*  output;
    FrameGrabber*              grabber;
    dispatch_queue_t           queue;
    int                        width;
    int                        height;
};

// ---------------------------------------------------------------------------
// Device enumeration
// ---------------------------------------------------------------------------

int list_video_devices(char*** names_out, char*** uids_out, int* count_out) {
    @autoreleasepool {
        NSArray<AVCaptureDeviceType>* types = @[
            AVCaptureDeviceTypeBuiltInWideAngleCamera,
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 140000
            AVCaptureDeviceTypeExternal,
#else
            AVCaptureDeviceTypeExternalUnknown,
#endif
        ];

        AVCaptureDeviceDiscoverySession* ds =
            [AVCaptureDeviceDiscoverySession
                discoverySessionWithDeviceTypes:types
                                      mediaType:AVMediaTypeVideo
                                       position:AVCaptureDevicePositionUnspecified];
        NSArray<AVCaptureDevice*>* devices = ds.devices;

        int n = (int)devices.count;
        *count_out = n;
        if (n == 0) {
            *names_out = NULL;
            *uids_out  = NULL;
            return 0;
        }

        *names_out = (char**)malloc(n * sizeof(char*));
        *uids_out  = (char**)malloc(n * sizeof(char*));
        for (int i = 0; i < n; i++) {
            (*names_out)[i] = strdup([devices[i].localizedName UTF8String]);
            (*uids_out)[i]  = strdup([devices[i].uniqueID UTF8String]);
        }
        return 0;
    }
}

void free_device_list(char** names, char** uids, int count) {
    for (int i = 0; i < count; i++) {
        free(names[i]);
        free(uids[i]);
    }
    free(names);
    free(uids);
}

// ---------------------------------------------------------------------------
// Open / Read / Close
// ---------------------------------------------------------------------------

CameraHandle* camera_open(const char* uid_cstr, int width, int height, int fps) {
    @autoreleasepool {
        NSString* uid = [NSString stringWithUTF8String:uid_cstr];
        AVCaptureDevice* device = [AVCaptureDevice deviceWithUniqueID:uid];
        if (!device) return NULL;

        NSError* err = nil;
        AVCaptureDeviceInput* input =
            [AVCaptureDeviceInput deviceInputWithDevice:device error:&err];
        if (!input || err) return NULL;

        AVCaptureSession* session = [[AVCaptureSession alloc] init];
        [session beginConfiguration];

        // Try to match a suitable preset.
        if (width <= 640 && height <= 480) {
            if ([session canSetSessionPreset:AVCaptureSessionPreset640x480])
                session.sessionPreset = AVCaptureSessionPreset640x480;
        } else if (width <= 1280 && height <= 720) {
            if ([session canSetSessionPreset:AVCaptureSessionPreset1280x720])
                session.sessionPreset = AVCaptureSessionPreset1280x720;
        } else {
            if ([session canSetSessionPreset:AVCaptureSessionPreset1920x1080])
                session.sessionPreset = AVCaptureSessionPreset1920x1080;
        }

        if ([session canAddInput:input])
            [session addInput:input];
        else {
            [session commitConfiguration];
            return NULL;
        }

        FrameGrabber* grabber = [[FrameGrabber alloc] initWithWidth:width
                                                             height:height];

        dispatch_queue_t q = dispatch_queue_create("photonforge.capture",
                                                   DISPATCH_QUEUE_SERIAL);

        AVCaptureVideoDataOutput* output =
            [[AVCaptureVideoDataOutput alloc] init];
        output.videoSettings = @{
            (id)kCVPixelBufferPixelFormatTypeKey : @(kCVPixelFormatType_32BGRA),
            (id)kCVPixelBufferWidthKey           : @(width),
            (id)kCVPixelBufferHeightKey          : @(height),
        };
        output.alwaysDiscardsLateVideoFrames = YES;
        [output setSampleBufferDelegate:grabber queue:q];

        if ([session canAddOutput:output])
            [session addOutput:output];
        else {
            [session commitConfiguration];
            return NULL;
        }

        [session commitConfiguration];

        // Set frame rate on the device — use the exact CMTime from the
        // closest supported AVFrameRateRange to avoid rejection.
        if ([device lockForConfiguration:&err]) {
            float targetFPS = (float)fps;
            float bestDelta = HUGE_VALF;
            AVFrameRateRange* bestRange = nil;

            for (AVFrameRateRange* range in device.activeFormat.videoSupportedFrameRateRanges) {
                // Each range may span min..max fps; pick the one whose
                // max rate is closest to our target.
                float delta = fabsf((float)range.maxFrameRate - targetFPS);
                if (delta < bestDelta) {
                    bestDelta = delta;
                    bestRange = range;
                }
            }

            if (bestRange) {
                // Use the range's own CMTime values — these are the exact
                // durations the device accepts.
                device.activeVideoMinFrameDuration = bestRange.minFrameDuration;
                device.activeVideoMaxFrameDuration = bestRange.maxFrameDuration;
            }
            [device unlockForConfiguration];
        }

        [session startRunning];

        CameraHandle* h = (CameraHandle*)calloc(1, sizeof(CameraHandle));
        h->session = session;
        h->input   = input;
        h->output  = output;
        h->grabber = grabber;
        h->queue   = q;
        h->width   = width;
        h->height  = height;
        return h;
    }
}

int camera_read_frame(CameraHandle* cam, uint8_t* buf, size_t buf_len) {
    if (!cam || !cam->grabber) return -1;
    return [cam->grabber readFrameInto:buf length:buf_len timeout_ms:2000];
}

int camera_actual_size(CameraHandle* cam, int* out_width, int* out_height) {
    if (!cam || !cam->grabber) return -1;
    [cam->grabber.lock lock];
    int aw = cam->grabber.actualWidth;
    int ah = cam->grabber.actualHeight;
    [cam->grabber.lock unlock];
    if (aw == 0 || ah == 0) return -1;
    *out_width  = aw;
    *out_height = ah;
    return 0;
}

void camera_signal_close(CameraHandle* cam) {
    if (!cam || !cam->grabber) return;
    [cam->grabber signalClose];
}

void camera_close(CameraHandle** cam_ptr) {
    if (!cam_ptr || !*cam_ptr) return;
    CameraHandle* cam = *cam_ptr;

    @autoreleasepool {
        // Signal the grabber to unblock any pending read before teardown.
        [cam->grabber signalClose];

        [cam->session stopRunning];

        // Remove inputs/outputs to release device lock.
        [cam->session beginConfiguration];
        for (AVCaptureInput*  i in cam->session.inputs)
            [cam->session removeInput:i];
        for (AVCaptureOutput* o in cam->session.outputs)
            [cam->session removeOutput:o];
        [cam->session commitConfiguration];

        // Drain the capture queue — ensures no delegate call is in-flight.
        dispatch_sync(cam->queue, ^{});

        cam->grabber = nil;
        cam->output  = nil;
        cam->input   = nil;
        cam->session = nil;
    }

    free(cam);
    *cam_ptr = NULL;
}
