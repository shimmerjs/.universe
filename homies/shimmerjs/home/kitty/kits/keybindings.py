#!/usr/bin/env python3
"""Kitten to print the current list of keyboard shortcuts (consists of BOTH single keys and key
sequences).
"""

import re
import json
from collections import defaultdict
from collections.abc import Sequence
from typing import Final, Union, TypeAlias

from kittens.tui.handler import result_handler
from kitty import fast_data_types
from kitty.boss import Boss
from kitty.options.types import Options as KittyOpts
from kitty.options.utils import KeyMap, KeyboardMode, KeyDefinition
from kitty.tab_bar import Formatter as fmt
from kitty.types import Shortcut, mod_to_names

Shortcut2Defn: TypeAlias = dict[Shortcut, str]
ShortcutRepr: TypeAlias = str
ActionMap: TypeAlias = dict[str, list[ShortcutRepr]]


def main(args: list[str]) -> Union[str, None]:
    pass


@result_handler(no_ui=True)
def handle_result(args: list[str], answer: str, target_window_id: int, boss: Boss):
    opts: KittyOpts = fast_data_types.get_options()

    output2 = [{"kitty_mod": "+".join(mod_to_names(opts.kitty_mod))}]
    output_categorized: dict[str, ActionMap] = defaultdict(lambda: defaultdict(list))
    for mode in opts.keyboard_modes.values():
        mode: KeyboardMode
        mode_name: str = mode.name or "default"
        mode_keymap: KeyMap = mode.keymap

        # set up keymaps (single keystrokes) + key sequences (combinations of keystrokes)
        key_mappings: Shortcut2Defn = {}
        for key, definitions in mode_keymap.items():
            key: SingleKey
            definitions: Sequence[KeyDefinition]

            for defn in definitions:
                action = defn.human_repr()
                if defn.is_sequence:
                    key_mappings[Shortcut((defn.trigger,) + defn.rest)] = action
                else:
                    key_mappings[Shortcut((key,))] = action

        # categorize the default mode shortcuts
        # because each action can have multiple shortcuts associated with it, we also attempt to
        # group shortcuts with the same actions together.
        for key, action in key_mappings.items():
            key2 = key.human_repr(kitty_mod=opts.kitty_mod)
            
            output2.append({"mode":mode_name, "keys":key2, "action":action})

    output2std = []
    for x in output2:
      output2std.append(json.dumps(x))
    return "\n".join(output2std)
