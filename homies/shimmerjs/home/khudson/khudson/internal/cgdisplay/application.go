package cgdisplay

/*
#cgo LDFLAGS: -framework AppKit -framework Carbon
#include "application_status.h"
int displayApplicationInit(struct displayApplicationStatus *status);
void displayApplicationRun(void);
void displayApplicationStop(void);
unsigned long long displayApplicationHeartbeat(void);
int displayApplicationSecureInput(void);
*/
import "C"
import "fmt"

// ApplicationInit must run on the process's initial, locked OS thread.
// It creates a prohibited-activation application with no windows.
func ApplicationInit() error {
	var status C.struct_displayApplicationStatus
	if code := int(C.displayApplicationInit(&status)); code != 0 {
		return &ApplicationError{Code: code, Before: int(status.before), After: int(status.after), Attempted: status.attempted != 0, Accepted: status.accepted != 0}
	}
	return nil
}
func ApplicationRun()  { C.displayApplicationRun() }
func ApplicationStop() { C.displayApplicationStop() }

func ApplicationHeartbeat() uint64 { return uint64(C.displayApplicationHeartbeat()) }

func ApplicationSecureInput() int { return int(C.displayApplicationSecureInput()) }

// ApplicationError retains both the requested transition and observed state.
type ApplicationError struct {
	Code, Before, After int
	Attempted, Accepted bool
}

func (e *ApplicationError) Error() string {
	return fmt.Sprintf("AppKit initialization status %d: policy before=%d after=%d attempted=%t accepted=%t", e.Code, e.Before, e.After, e.Attempted, e.Accepted)
}
func (e *ApplicationError) Kind() string {
	switch e.Code {
	case 1:
		return "appkit_wrong_thread"
	case 2:
		return "appkit_activation_policy"
	default:
		return "appkit_unknown"
	}
}

func (e *ApplicationError) NativeCode() int { return e.Code }
