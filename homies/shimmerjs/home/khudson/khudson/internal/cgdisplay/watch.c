#include <string.h>
#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include "_cgo_export.h"

// The run loop the watch thread parks on, retained so cgdisplayWatchStop can
// reach it from another thread (CFRunLoopStop is thread-safe).
static CFRunLoopRef gLoop = NULL;

// reconfigCB fires once per affected display per reconfiguration. The Begin
// pass is dropped: it announces a change that has not landed yet, so the
// geometry a reader would see is still the old arrangement.
static void reconfigCB(CGDirectDisplayID display, CGDisplayChangeSummaryFlags flags, void *userInfo) {
	(void)display;
	(void)userInfo;
	if (flags & kCGDisplayBeginConfigurationFlag) {
		return;
	}
	cgdisplayChanged();
}

// cgdisplayWatchRun registers the callback and parks the calling thread on
// its run loop until cgdisplayWatchStop. The keepalive source exists because
// CFRunLoopRun returns immediately when the loop has no input source, and
// CGDisplayRegisterReconfigurationCallback does not promise to install one.
void cgdisplayWatchRun(void) {
	CGDisplayRegisterReconfigurationCallback(reconfigCB, NULL);

	CFRunLoopSourceContext srcCtx;
	memset(&srcCtx, 0, sizeof(srcCtx));
	CFRunLoopSourceRef keepalive = CFRunLoopSourceCreate(kCFAllocatorDefault, 0, &srcCtx);
	CFRunLoopAddSource(CFRunLoopGetCurrent(), keepalive, kCFRunLoopDefaultMode);

	gLoop = (CFRunLoopRef)CFRetain(CFRunLoopGetCurrent());
	cgdisplayWatchReady();
	CFRunLoopRun();

	CGDisplayRemoveReconfigurationCallback(reconfigCB, NULL);
	CFRunLoopRemoveSource(gLoop, keepalive, kCFRunLoopDefaultMode);
	CFRelease(keepalive);
	CFRelease(gLoop);
	gLoop = NULL;
}

void cgdisplayWatchStop(void) {
	if (gLoop != NULL) {
		CFRunLoopStop(gLoop);
	}
}
