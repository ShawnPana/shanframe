// One still of the main display as PNG at point resolution (so the image's
// pixel coordinates are the coordinates input events take), via
// ScreenCaptureKit's screenshot API. Screen Recording permission covers it.
#import <Foundation/Foundation.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreGraphics/CoreGraphics.h>
#import <ImageIO/ImageIO.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

int sfStill(uint8_t **out, int *n, int *w, int *h) {
	__block CGImageRef img = NULL;
	__block int pw = 0, ph = 0;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[SCShareableContent getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:YES
		completionHandler:^(SCShareableContent *content, NSError *err) {
		if (!content || content.displays.count == 0) { dispatch_semaphore_signal(sem); return; }
		SCDisplay *d = content.displays.firstObject;
		for (SCDisplay *x in content.displays) if (x.displayID == CGMainDisplayID()) d = x;
		pw = (int)d.width; ph = (int)d.height; // points
		SCContentFilter *f = [[SCContentFilter alloc] initWithDisplay:d excludingWindows:@[]];
		SCStreamConfiguration *cfg = [SCStreamConfiguration new];
		cfg.width = pw; cfg.height = ph;
		cfg.showsCursor = YES;
		[SCScreenshotManager captureImageWithFilter:f configuration:cfg
			completionHandler:^(CGImageRef image, NSError *e) {
			if (image) img = CGImageRetain(image);
			dispatch_semaphore_signal(sem);
		}];
	}];
	dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 8 * NSEC_PER_SEC));
	if (!img) return 0;
	CFMutableDataRef data = CFDataCreateMutable(NULL, 0);
	CGImageDestinationRef dst = CGImageDestinationCreateWithData(data, CFSTR("public.png"), 1, NULL);
	if (!dst) { CGImageRelease(img); CFRelease(data); return 0; }
	CGImageDestinationAddImage(dst, img, NULL);
	int ok = CGImageDestinationFinalize(dst);
	CFRelease(dst);
	CGImageRelease(img);
	if (!ok) { CFRelease(data); return 0; }
	*n = (int)CFDataGetLength(data);
	*out = malloc(*n);
	memcpy(*out, CFDataGetBytePtr(data), *n);
	CFRelease(data);
	*w = pw; *h = ph;
	return 1;
}
