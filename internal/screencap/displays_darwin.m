// Displays are numbered the way people count them: the main display is 1,
// then the others left to right (top to bottom for ties), by their position
// in the global desktop. Both the CoreGraphics list (what we report) and
// ScreenCaptureKit's list (what we capture from) are sorted the same way, so
// "display 2" means the same monitor everywhere.
#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

static NSComparisonResult sfDisplayOrder(CGDirectDisplayID a, CGDirectDisplayID b) {
    CGDirectDisplayID m = CGMainDisplayID();
    if (a == b) return NSOrderedSame;
    if (a == m) return NSOrderedAscending;
    if (b == m) return NSOrderedDescending;
    CGRect ra = CGDisplayBounds(a), rb = CGDisplayBounds(b);
    if (ra.origin.x != rb.origin.x) return ra.origin.x < rb.origin.x ? NSOrderedAscending : NSOrderedDescending;
    if (ra.origin.y != rb.origin.y) return ra.origin.y < rb.origin.y ? NSOrderedAscending : NSOrderedDescending;
    return a < b ? NSOrderedAscending : NSOrderedDescending;
}

// sfDisplayAt picks display number n (1-based; 0 means the main display)
// from ScreenCaptureKit's list, or nil when there is no such display.
SCDisplay *sfDisplayAt(NSArray<SCDisplay *> *displays, int n) {
    NSArray<SCDisplay *> *sorted = [displays sortedArrayUsingComparator:^NSComparisonResult(SCDisplay *a, SCDisplay *b) {
        return sfDisplayOrder(a.displayID, b.displayID);
    }];
    if (n <= 0) n = 1;
    if (n > (int)sorted.count) return nil;
    return sorted[n - 1];
}

// the user-facing name macOS shows in System Settings ("Built-in Retina
// Display", "LG ULTRAFINE"); empty when AppKit has no screen for the id
static NSString *sfDisplayName(CGDirectDisplayID id) {
    for (NSScreen *s in NSScreen.screens) {
        NSNumber *num = s.deviceDescription[@"NSScreenNumber"];
        if (num && num.unsignedIntValue == id) {
            if (@available(macOS 10.15, *)) return s.localizedName;
            break;
        }
    }
    return @"";
}

// sfDisplaysJSON lists the attached displays in display order as JSON
// [{n,name,w,h,x,y,main}] (points, global desktop coordinates). The caller
// frees the string.
char *sfDisplaysJSON(void) {
    uint32_t ids[16], n = 0;
    CGGetActiveDisplayList(16, ids, &n);
    for (uint32_t i = 1; i < n; i++)
        for (uint32_t j = i; j > 0 && sfDisplayOrder(ids[j - 1], ids[j]) == NSOrderedDescending; j--) {
            uint32_t t = ids[j]; ids[j] = ids[j - 1]; ids[j - 1] = t;
        }
    NSMutableArray *out = [NSMutableArray arrayWithCapacity:n];
    for (uint32_t i = 0; i < n; i++) {
        CGRect b = CGDisplayBounds(ids[i]);
        NSString *name = sfDisplayName(ids[i]);
        if (name.length == 0) name = [NSString stringWithFormat:@"Display %u", i + 1];
        [out addObject:@{ @"n": @(i + 1), @"name": name, @"w": @(b.size.width), @"h": @(b.size.height),
                          @"x": @(b.origin.x), @"y": @(b.origin.y), @"main": [NSNumber numberWithBool:ids[i] == CGMainDisplayID()] }];
    }
    NSData *json = [NSJSONSerialization dataWithJSONObject:out options:0 error:nil];
    if (!json) return strdup("[]");
    char *s = malloc(json.length + 1);
    memcpy(s, json.bytes, json.length);
    s[json.length] = 0;
    return s;
}
