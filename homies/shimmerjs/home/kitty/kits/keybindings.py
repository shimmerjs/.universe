#!/usr/bin/env python3
"""Kitten to print the current list of keyboard shortcuts (consists of BOTH single keys and key
sequences).
"""

import json
from typing import Union

from kittens.tui.handler import result_handler
from kitty import fast_data_types
from kitty.boss import Boss
from kitty.config import load_config
from kitty.options.types import Options as KittyOpts
from kitty.types import Shortcut, mod_to_names


def main(args: list[str]) -> Union[str, None]:
    pass


def collect_bindings(opts: KittyOpts) -> dict[tuple[str, str], dict]:
    """Extract keybindings from an Options object, keyed by (mode, keys)."""
    bindings: dict[tuple[str, str], dict] = {}
    for mode in opts.keyboard_modes.values():
        mode_name = mode.name or "default"
        for key, definitions in mode.keymap.items():
            for defn in definitions:
                action = defn.human_repr()
                if defn.is_sequence:
                    shortcut = Shortcut((defn.trigger,) + defn.rest)
                else:
                    shortcut = Shortcut((key,))
                keys = shortcut.human_repr(kitty_mod=opts.kitty_mod)
                bindings[(mode_name, keys)] = {
                    "mode": mode_name,
                    "keys": keys,
                    "action": action,
                }
    return bindings


@result_handler(no_ui=True)
def handle_result(args: list[str], answer: str, target_window_id: int, boss: Boss):
    live_opts: KittyOpts = fast_data_types.get_options()
    disk_opts: KittyOpts = load_config(*live_opts.all_config_paths, overrides=live_opts.config_overrides)

    merged = collect_bindings(disk_opts)
    merged.update(collect_bindings(live_opts))

    output = [{"kitty_mod": "+".join(mod_to_names(live_opts.kitty_mod))}]
    output.extend(merged.values())

    return "\n".join(json.dumps(x) for x in output)
