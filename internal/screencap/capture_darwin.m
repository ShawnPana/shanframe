// capture_darwin.m — ScreenCaptureKit → VideoToolbox H.264, Annex-B frames.
// The Go side (screencap_darwin.go) receives each encoded frame via the
// exported callback shanframeFrame(id, data, len, keyframe, ptsMs).
//
// One capture session per screen stream. The cursor is NOT composited into
// the video: viewers draw a zero-latency local sprite, fed by sfCursorPNG.

#import <Foundation/Foundation.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <VideoToolbox/VideoToolbox.h>
#import <CoreGraphics/CoreGraphics.h>
#include <stdint.h>

extern void shanframeFrame(long id_, const uint8_t *buf, int len, int key, int64_t ptsMs);

@interface SFCapture : NSObject <SCStreamOutput, SCStreamDelegate>
@property(nonatomic) long goID;
@property(nonatomic) SCStream *stream;
@property(nonatomic) VTCompressionSessionRef vt;
@property(nonatomic) dispatch_queue_t queue;
@property(nonatomic) int outW, outH;
@property(nonatomic) BOOL stopped;
@property(atomic) BOOL forceKey;
@end

static void sfCompressed(void *ctx, void *src, OSStatus status, VTEncodeInfoFlags flags,
                         CMSampleBufferRef sb) {
    if (status != noErr || sb == NULL || !CMSampleBufferDataIsReady(sb)) return;
    SFCapture *cap = (__bridge SFCapture *)ctx;
    if (cap.stopped) return;

    BOOL key = YES;
    CFArrayRef atts = CMSampleBufferGetSampleAttachmentsArray(sb, false);
    if (atts && CFArrayGetCount(atts) > 0) {
        CFDictionaryRef att = CFArrayGetValueAtIndex(atts, 0);
        key = !CFDictionaryContainsKey(att, kCMSampleAttachmentKey_NotSync);
    }

    NSMutableData *out = [NSMutableData dataWithCapacity:64 * 1024];
    static const uint8_t start[4] = {0, 0, 0, 1};
    if (key) { // prepend SPS/PPS so every keyframe is self-contained
        CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(sb);
        size_t count = 0;
        CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, 0, NULL, NULL, &count, NULL);
        for (size_t i = 0; i < count; i++) {
            const uint8_t *ps; size_t pslen;
            if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, i, &ps, &pslen, NULL, NULL) == noErr) {
                [out appendBytes:start length:4];
                [out appendBytes:ps length:pslen];
            }
        }
    }
    CMBlockBufferRef bb = CMSampleBufferGetDataBuffer(sb);
    size_t len = 0; char *ptr = NULL;
    if (CMBlockBufferGetDataPointer(bb, 0, NULL, &len, &ptr) != kCMBlockBufferNoErr) return;
    size_t off = 0; // AVCC (4-byte lengths) → Annex-B (start codes)
    while (off + 4 <= len) {
        uint32_t n = CFSwapInt32BigToHost(*(uint32_t *)(ptr + off));
        if (off + 4 + n > len) break;
        [out appendBytes:start length:4];
        [out appendBytes:ptr + off + 4 length:n];
        off += 4 + n;
    }

    CMTime pts = CMSampleBufferGetPresentationTimeStamp(sb);
    int64_t ms = (int64_t)(CMTimeGetSeconds(pts) * 1000.0);
    shanframeFrame(cap.goID, out.bytes, (int)out.length, key ? 1 : 0, ms);
}

@implementation SFCapture
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb
        ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen || self.stopped) return;
    CVImageBufferRef img = CMSampleBufferGetImageBuffer(sb);
    if (!img) return; // SCK sends idle/status frames without pixels
    CFDictionaryRef props = NULL;
    if (self.forceKey) { // a viewer lost packets and asked for a fresh keyframe
        self.forceKey = NO;
        const void *k[] = { kVTEncodeFrameOptionKey_ForceKeyFrame };
        const void *v[] = { kCFBooleanTrue };
        props = CFDictionaryCreate(NULL, k, v, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    }
    VTCompressionSessionEncodeFrame(self.vt, img,
        CMSampleBufferGetPresentationTimeStamp(sb), kCMTimeInvalid, props, NULL, NULL);
    if (props) CFRelease(props);
}
- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    self.stopped = YES;
}
@end

int sfPreflight(void) { return CGPreflightScreenCaptureAccess() ? 1 : 0; }
int sfRequest(void) { return CGRequestScreenCaptureAccess() ? 1 : 0; }

// sfBegin wires the encoder + stream for a prepared filter and size.
static void *sfBegin(long goID, SCContentFilter *filter, SCStreamConfiguration *conf,
                     int w, int h, int fps, int bitrate);

// sfStart begins capture of the main display. Returns a retained handle
// (release with sfStop) or 0 on failure; *outW/*outH get the encoded size.
void *sfStart(long goID, int maxDim, int fps, int bitrate, int *outW, int *outH) {
    __block SCDisplay *display = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent *content, NSError *err) {
        for (SCDisplay *d in content.displays) {
            if (d.displayID == CGMainDisplayID()) { display = d; break; }
        }
        if (!display) display = content.displays.firstObject;
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC));
    if (!display) return NULL;

    // native pixel size, capped to maxDim on the long side
    CGDisplayModeRef mode = CGDisplayCopyDisplayMode(display.displayID);
    int pw = mode ? (int)CGDisplayModeGetPixelWidth(mode) : (int)display.width * 2;
    int ph = mode ? (int)CGDisplayModeGetPixelHeight(mode) : (int)display.height * 2;
    if (mode) CGDisplayModeRelease(mode);
    double scale = 1.0;
    int longSide = pw > ph ? pw : ph;
    if (longSide > maxDim) scale = (double)maxDim / longSide;
    if (scale > 0.4 && scale < 1.0) scale = 0.5; // retina: exact half keeps text crisp
    int w = ((int)(pw * scale)) & ~1, h = ((int)(ph * scale)) & ~1;
    *outW = w; *outH = h;

    SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];
    SCStreamConfiguration *conf = [SCStreamConfiguration new];
    conf.width = w; conf.height = h;
    conf.minimumFrameInterval = CMTimeMake(1, fps);
    conf.pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange;
    conf.showsCursor = NO; // viewers draw a local sprite (zero-latency); sfCursorPNG streams the shape
    conf.queueDepth = 3;
    return sfBegin(goID, filter, conf, w, h, fps, bitrate);
}


static void *sfBegin(long goID, SCContentFilter *filter, SCStreamConfiguration *conf,
                     int w, int h, int fps, int bitrate) {
    SFCapture *cap = [SFCapture new];
    cap.goID = goID; cap.outW = w; cap.outH = h;
    cap.queue = dispatch_queue_create("shanframe.screencap", DISPATCH_QUEUE_SERIAL);

    VTCompressionSessionRef vt = NULL;
    if (VTCompressionSessionCreate(NULL, w, h, kCMVideoCodecType_H264, NULL, NULL, NULL,
                                   sfCompressed, (__bridge void *)cap, &vt) != noErr) return NULL;
    VTSessionSetProperty(vt, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
    VTSessionSetProperty(vt, kVTCompressionPropertyKey_ProfileLevel, kVTProfileLevel_H264_Baseline_AutoLevel);
    VTSessionSetProperty(vt, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
    int keyInt = fps * 2;
    VTSessionSetProperty(vt, kVTCompressionPropertyKey_MaxKeyFrameInterval, (__bridge CFTypeRef)@(keyInt));
    VTSessionSetProperty(vt, kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, (__bridge CFTypeRef)@(2.0));
    VTSessionSetProperty(vt, kVTCompressionPropertyKey_AverageBitRate, (__bridge CFTypeRef)@(bitrate));
    // hard cap on bursts: a giant keyframe otherwise stalls the link for
    // hundreds of ms and reads as periodic lag on the viewer
    NSArray *limits = @[ @(bitrate / 8 / 4), @(0.25) ]; // bytes per quarter second
    VTSessionSetProperty(vt, kVTCompressionPropertyKey_DataRateLimits, (__bridge CFTypeRef)limits);
    VTSessionSetProperty(vt, kVTCompressionPropertyKey_ExpectedFrameRate, (__bridge CFTypeRef)@(fps));
    VTSessionSetProperty(vt, kVTCompressionPropertyKey_MaxFrameDelayCount, (__bridge CFTypeRef)@(1));
    VTCompressionSessionPrepareToEncodeFrames(vt);
    cap.vt = vt;

    SCStream *stream = [[SCStream alloc] initWithFilter:filter configuration:conf delegate:cap];
    NSError *err = nil;
    [stream addStreamOutput:cap type:SCStreamOutputTypeScreen sampleHandlerQueue:cap.queue error:&err];
    if (err) { VTCompressionSessionInvalidate(vt); return NULL; }
    cap.stream = stream;

    __block BOOL ok = NO;
    dispatch_semaphore_t sem2 = dispatch_semaphore_create(0);
    [stream startCaptureWithCompletionHandler:^(NSError *e) {
        ok = (e == nil);
        dispatch_semaphore_signal(sem2);
    }];
    dispatch_semaphore_wait(sem2, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC));
    if (!ok) { VTCompressionSessionInvalidate(vt); return NULL; }

    return (void *)CFBridgingRetain(cap);
}

void sfForceKey(void *handle) {
    if (!handle) return;
    ((__bridge SFCapture *)handle).forceKey = YES;
}

void sfStop(void *handle) {
    if (!handle) return;
    SFCapture *cap = CFBridgingRelease(handle);
    cap.stopped = YES;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [cap.stream stopCaptureWithCompletionHandler:^(NSError *e) { dispatch_semaphore_signal(sem); }];
    dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
    if (cap.vt) {
        VTCompressionSessionCompleteFrames(cap.vt, kCMTimeInvalid);
        VTCompressionSessionInvalidate(cap.vt);
        CFRelease(cap.vt);
        cap.vt = NULL;
    }
}
