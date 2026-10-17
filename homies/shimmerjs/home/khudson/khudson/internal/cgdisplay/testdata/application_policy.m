#import "../application_policy.h"
#include <stdio.h>

// No NSApplication is created. This exercises the production native decision
// against an object exposing only the two documented policy methods.
@interface FakePolicy : NSObject <KhudsonApplicationPolicy>
@property NSApplicationActivationPolicy current;
@property NSApplicationActivationPolicy result;
@property BOOL accepted;
@property int calls;
@end
@implementation FakePolicy
- (NSApplicationActivationPolicy)activationPolicy { return self.current; }
- (BOOL)setActivationPolicy:(NSApplicationActivationPolicy)policy {
    if (policy != NSApplicationActivationPolicyProhibited) abort();
    self.calls++;
    self.current = self.result;
    return self.accepted;
}
@end

int main(void) {
    @autoreleasepool {
        struct { int initial, final, accepted, wantCode, wantCalls; } cases[] = {
            {2, 2, 0, 0, 0}, // already prohibited: a redundant setter would fail
            {2, 0, 1, 0, 0}, // never invoke the setter when already correct
            {0, 2, 1, 0, 1},
            {1, 2, 1, 0, 1},
            {0, 2, 0, 0, 1}, // observed state is authoritative
            {0, 0, 0, 2, 1},
            {1, 1, 0, 2, 1},
            {0, 0, 1, 2, 1}, // accepted does not hide a mismatched postcondition
        };
        for (unsigned i = 0; i < sizeof(cases)/sizeof(cases[0]); i++) {
            FakePolicy *app = [FakePolicy new];
            app.current = cases[i].initial;
            app.result = cases[i].final;
            app.accepted = cases[i].accepted;
            struct displayApplicationStatus status;
            int code = displayEnsureProhibited(app, &status);
            if (code != cases[i].wantCode || app.calls != cases[i].wantCalls ||
                status.before != cases[i].initial || status.after != app.current ||
                status.attempted != app.calls) {
                fprintf(stderr, "activation policy case %u failed: code=%d calls=%d before=%d after=%d\n",
                        i, code, app.calls, status.before, status.after);
                return 1;
            }
            printf("activation policy case %u passed\n", i);
        }
    }
    return 0;
}
