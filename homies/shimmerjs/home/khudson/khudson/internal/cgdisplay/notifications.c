#include <CoreGraphics/CoreGraphics.h>
#include <stdint.h>
#include "_cgo_export.h"

static void displayChanged(CGDirectDisplayID display,
                           CGDisplayChangeSummaryFlags flags, void *token) {
    (void)display;
    displayNotification((uintptr_t)token, (uint32_t)flags);
}

int32_t displayNotificationsRegister(uintptr_t token) {
    return CGDisplayRegisterReconfigurationCallback(displayChanged, (void *)token);
}

int32_t displayNotificationsRemove(uintptr_t token) {
    return CGDisplayRemoveReconfigurationCallback(displayChanged, (void *)token);
}
