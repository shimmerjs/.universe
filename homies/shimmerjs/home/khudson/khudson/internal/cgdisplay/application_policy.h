#import <AppKit/AppKit.h>
#include "application_status.h"

@protocol KhudsonApplicationPolicy
- (NSApplicationActivationPolicy)activationPolicy;
- (BOOL)setActivationPolicy:(NSApplicationActivationPolicy)policy;
@end

// Unbundled helpers already default to prohibited. Only request a transition
// when necessary, then verify the actual policy independently of its return.
static inline int displayEnsureProhibited(id<KhudsonApplicationPolicy> app,
                                         struct displayApplicationStatus *status) {
    status->before = status->after = (int)[app activationPolicy];
    status->attempted = status->accepted = 0;
    if (status->before == NSApplicationActivationPolicyProhibited) return 0;
    status->attempted = 1;
    status->accepted = [app setActivationPolicy:NSApplicationActivationPolicyProhibited] ? 1 : 0;
    status->after = (int)[app activationPolicy];
    return status->after == NSApplicationActivationPolicyProhibited ? 0 : 2;
}
