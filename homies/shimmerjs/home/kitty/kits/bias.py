#!/usr/bin/env python3
"""Kitten to step the active split's bias by a fixed amount, wrapping round.

`layout_action bias N` is absolute and takes an int, so kitty on its own can
only snap to preset ratios -- there is no relative form, and the one relative
path (resize_window) counts in CELLS, not percent. This reads the live bias
off the layout tree and writes back the next stop, which is the piece kitty
does not provide.

It runs IN the kitty process: handle_result gets the Boss, so the layout
object is right there. Nothing is guessed and nothing is cached, so it cannot
drift out of step with a bias changed by any other means (drag, cmd+r,
equalize, maximize).

    map kitty_mod+b kitten kits/bias.py 20      # active pane grows 20% a press
    map kitty_mod+shift+b kitten kits/bias.py -20

Only the splits layout has a bias tree; anywhere else this is a no-op.
"""

from typing import Union

from kittens.tui.handler import result_handler
from kitty.boss import Boss

# Never hand a pane the whole pair. 0 or 100 collapses one side to nothing,
# which reads as a window that vanished rather than a bias that wrapped.
MIN_BIAS = 10
MAX_BIAS = 90


def main(args: list[str]) -> Union[str, None]:
    pass


def step_of(args: list[str]) -> int:
    for a in args[1:]:
        try:
            return int(a)
        except ValueError:
            continue
    return 20


@result_handler(no_ui=True)
def handle_result(args: list[str], answer: str, target_window_id: int, boss: Boss) -> None:
    tab = boss.active_tab
    if tab is None:
        return
    layout = tab.current_layout
    # splits is the only layout with a pair tree; ask rather than assume, so
    # this stays a no-op instead of an exception if a layout is re-enabled
    root = getattr(layout, "pairs_root", None)
    if root is None:
        return
    group = tab.windows.active_group
    if group is None:
        return
    pair = root.pair_for_window(group.id)
    if pair is None or pair.is_redundant:
        return

    # Pair.bias is always the share belonging to `one`; set_bias mirrors it for
    # `two`. Both ends of this function talk in the ACTIVE pane's share, which
    # is what the keypress means and what makes one binding work on either side
    # of a split.
    share = pair.bias if group.id == pair.one else 1.0 - pair.bias
    nxt = round(share * 100) + step_of(args)

    span = MAX_BIAS - MIN_BIAS
    nxt = MIN_BIAS + (nxt - MIN_BIAS) % (span + 1)

    tab.layout_action("bias", (str(nxt),))
