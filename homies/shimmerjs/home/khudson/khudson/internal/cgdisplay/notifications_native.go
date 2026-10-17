package cgdisplay

/*
#include <stdint.h>
int32_t displayNotificationsRegister(uintptr_t token);
int32_t displayNotificationsRemove(uintptr_t token);
*/
import "C"

// Subscribe registers on the calling thread. Call it from the integration's
// event-processing thread, before its initial display snapshot. It does not
// create an AppKit application or run a CFRunLoop.
func Subscribe() (*Subscription, error) {
	return subscribe(notificationAPI{
		register: func(id uintptr) int32 { return int32(C.displayNotificationsRegister(C.uintptr_t(id))) },
		remove:   func(id uintptr) int32 { return int32(C.displayNotificationsRemove(C.uintptr_t(id))) },
	})
}

//export displayNotification
func displayNotification(token C.uintptr_t, flags C.uint32_t) {
	deliverNotification(uintptr(token), uint32(flags))
}
