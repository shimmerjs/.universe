package bus

import (
	"fmt"

	"github.com/shimmerjs/khudson/khudson/internal/effectrun"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/provider"
)

func (b *Bus) newEffectRunner() *effectrun.Host {
	return effectrun.New(effectrun.Hooks{Accept: b.acceptAction, Transition: b.transitionAction, Finish: b.finishAction, Reject: b.rejectAction, Broadcast: b.broadcast, Failed: b.actFailed, Start: func() func([]string) (func() error, error) { return b.execStart }})
}
func (b *Bus) runEffects() { b.effects.Run() }
func (b *Bus) dispatchEffects(source string, effects []module.Effect) error {
	return b.dispatchEffectsFor(proto.Msg{}, source, effects)
}
func (b *Bus) dispatchEffectsFor(parent proto.Msg, source string, effects []module.Effect) error {
	if b.effects == nil {
		if len(effects) == 0 {
			return nil
		}
		return fmt.Errorf("effect owner unavailable")
	}
	return b.effects.Dispatch(parent, source, effects)
}
func (b *Bus) DispatchTransition(source string, fn func() []module.Effect) error {
	return b.dispatchTransitionFor(proto.Msg{}, source, fn)
}
func (b *Bus) dispatchTransitionFor(parent proto.Msg, source string, fn func() []module.Effect) error {
	if b.effects == nil {
		return b.dispatchEffects(source, fn())
	}
	return b.effects.Transition(parent, source, fn)
}
func (b *Bus) startEffect(argv []string, final bool) (func() error, error) {
	return b.effects.Start(argv, final)
}

var effectArgvAllowed = effectrun.ArgvAllowed

const effectKillGrace = effectrun.KillGrace
