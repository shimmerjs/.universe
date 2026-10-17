#import "application_policy.h"
#import <Carbon/Carbon.h>
#import <dispatch/dispatch.h>
#include <stdatomic.h>

static _Atomic unsigned long long heartbeat;
static _Atomic int secureInput = -1;
static NSTimer *heartbeatTimer;

unsigned long long displayApplicationHeartbeat(void) { return atomic_load(&heartbeat); }
int displayApplicationSecureInput(void) { return atomic_load(&secureInput); }

int displayApplicationInit(struct displayApplicationStatus *status) {
    *status = (struct displayApplicationStatus){.before = -1, .after = -1};
    if (![NSThread isMainThread]) return 1;
    @autoreleasepool {
        [NSApplication sharedApplication];
        int code = displayEnsureProhibited((id<KhudsonApplicationPolicy>)NSApp, status);
        if (code != 0) return code;
        // Carbon documents this probe as not thread safe. Only this main
        // thread samples it; Go reads the atomic cached value.
        atomic_store(&secureInput, IsSecureEventInputEnabled() ? 1 : 0);
        heartbeatTimer = [NSTimer scheduledTimerWithTimeInterval:30 repeats:YES block:^(NSTimer *timer) {
            atomic_fetch_add(&heartbeat, 1);
            atomic_store(&secureInput, IsSecureEventInputEnabled() ? 1 : 0);
        }];
    }
    return 0;
}

void displayApplicationRun(void) {
    @autoreleasepool {
        [NSApp run];
        [heartbeatTimer invalidate];
        heartbeatTimer = nil;
    }
}

void displayApplicationStop(void) {
    // stop alone does not wake an event loop blocked in nextEvent. Queue
    // the stop even before run starts, then wake it with an app-local event.
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp stop:nil];
        [NSApp postEvent:[NSEvent otherEventWithType:NSEventTypeApplicationDefined
            location:NSZeroPoint modifierFlags:0 timestamp:0 windowNumber:0
            context:nil subtype:0 data1:0 data2:0] atStart:YES];
    });
}
