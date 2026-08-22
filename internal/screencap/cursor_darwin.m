// The current system cursor as a PNG — viewers draw the pointer locally
// (zero-latency sprite) and these shape updates keep it honest.
#import <AppKit/AppKit.h>
#import <ImageIO/ImageIO.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

// sfCursorPNG returns the cursor image (malloc'd PNG; caller frees) with
// hotspot and display size in points. Returns 1 on success.
int sfCursorPNG(uint8_t **out, int *outLen, double *hotX, double *hotY, double *w, double *h) {
    @autoreleasepool {
        NSCursor *cur = [NSCursor currentSystemCursor];
        if (!cur || !cur.image) return 0;
        NSImage *img = cur.image;
        CGImageRef cg = [img CGImageForProposedRect:NULL context:nil hints:nil];
        if (!cg) return 0;
        CFMutableDataRef data = CFDataCreateMutable(NULL, 0);
        CGImageDestinationRef dest = CGImageDestinationCreateWithData(data, CFSTR("public.png"), 1, NULL);
        if (!dest) { CFRelease(data); return 0; }
        CGImageDestinationAddImage(dest, cg, NULL);
        bool ok = CGImageDestinationFinalize(dest);
        CFRelease(dest);
        if (!ok) { CFRelease(data); return 0; }
        int len = (int)CFDataGetLength(data);
        *out = malloc(len);
        memcpy(*out, CFDataGetBytePtr(data), len);
        *outLen = len;
        CFRelease(data);
        *hotX = cur.hotSpot.x; *hotY = cur.hotSpot.y;
        *w = img.size.width; *h = img.size.height;
        return 1;
    }
}
