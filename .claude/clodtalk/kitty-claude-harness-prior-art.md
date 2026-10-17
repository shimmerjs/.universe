# kitty + Claude Code: prior-art map and harness build targets

Compiled from a fan-out research run (aw-research: fanout=6, passes=2, verify=3,
breadth=web+docs+code; ~907 agents, 253 claims the workflow marked verified, 0
unverified). Treat as SOURCED-BUT-UNCONFIRMED: the workflow ran its own
3-vote adversarial verification, but I have not independently re-checked each
claim. Anything version-pinned (section E) must be verified against YOUR
installed `claude --version` / `kitten --version` before you build on it. The
`[N]` markers are the run's internal citation indices; the resolvable anchor
URLs are listed at the bottom, and the full per-claim URL list lives in the
workflow transcript if you want a specific one.

---

## Verdict up front

Two rich, mostly-disjoint extensibility surfaces. The Claude Code multi-agent
ecosystem standardized on **tmux as a dumb PTY multiplexer** (scrape panes,
inject keystrokes). The **kitty-native** surface -- in-process kittens,
watchers, user-vars, graphics protocol, panels -- is **almost entirely
unexploited by AI harnesses**. That gap is the opportunity, and it lines up with
what you already run (kitty + Claude Code + a rich statusline, nix-managed).

---

## A. The two extensibility surfaces (what to build ON)

### kitty side -- four seam families

1. **Remote control (the pull/write spine).** JSON-over-DCS or the standalone
   `kitten @` binary over a unix socket (`--listen-on unix:/tmp/x` + `--to`).
   ~40-50 subcommands: `launch`, `send-text`/`send-key`, `get-text`, `ls` (full
   window tree as JSON), `focus-window`, `set-user-vars`, `set-tab-color`,
   `action` (run any mappable action -- the escape hatch), `broadcast` (fan a
   keystroke to N windows). [15][36][105][191][192][212]
2. **Watchers (the push/event spine).** Per-window or global Python modules:
   `on_load`, `on_resize`, `on_focus_change`, `on_close`, `on_set_user_var`,
   `on_title_change`, `on_cmd_startstop` (is_start/cmdline/time),
   `on_color_scheme_preference_change`; global-only `on_tab_bar_dirty`,
   `on_quit`. Each gets `(boss, window, data)` and can call
   `boss.call_remote_control(...)`. **No AI harness in the wild uses this.**
   [6][20][38][58][109][238]
3. **Custom kittens (the in-process privileged seam).** `main(args)->str` runs
   in an overlay; `handle_result(...)` runs INSIDE the kitty process with the
   live `Boss`. `@result_handler(type_of_input=text|ansi|history|screen|output|
   selection)` auto-feeds pane content; `no_ui=True` runs headless. Strictly
   more powerful than remote control. [3][4][17][18][39][135][218][219]
4. **Rendering / IO primitives.** Graphics protocol (inline raster, z-index
   below/above text, animation, Unicode placeholders to survive tmux/vim);
   panel kitten (desktop dock / sidebar / background / overlay; Wayland 0.34,
   **macOS 0.42+**); quick-access-terminal (Quake drop-down); OSC 99
   notifications (bidirectional -- buttons + click-report back to the program);
   OSC 1337 SetUserVar (in-band self-tagging); OSC 133 / OSC 7 shell
   integration; text-sizing protocol for big headline glyphs (0.40+);
   open_actions.conf for click-to-action on hyperlinks.
   [12][22][41][65][89][114][197][198][199][11][21][42][61][116][156][200][62][91][117][178][201][23][40][87][118][137][222][237][88][111][217][112][152][203][251][136][204]

### Claude Code side -- the data feeds + control points

- **statusLine**: rich JSON on stdin (model, cost, context_window %,
  rate_limits, effort, pr, worktree, agent.name, session_id...), multi-line
  ANSI + OSC 8 out, event-driven (300ms debounce) + optional `refreshInterval`
  >= 1s. CANNOT read `tput cols` -- use `COLUMNS`/`LINES` env (v2.1.153+).
  [7][8][27][66][92][122][138][158][185][243]
- **subagentStatusLine**: separate seam, per-subagent rows with `tasks[]`
  (id/name/type/status/tokenCount/cwd...), one JSON line per row. Native data
  feed for a teammate dashboard. [9][28][46][95][159][242]
- **hooks**: ~30 lifecycle events (SessionStart, PreToolUse, Notification,
  Stop, SubagentStart/Stop, TaskCreated/Completed, TeammateIdle, CwdChanged,
  FileChanged, WorktreeCreate/Remove...). Five handler types:
  command/http/mcp_tool/prompt/agent. The **`terminalSequence` output field**
  (OSC 0/1/2/9/99/777 + BEL) is a sanctioned channel for a hook to fire kitty
  notifications/titles with no TTY. Notification matcher distinguishes
  `permission_prompt` vs `idle_prompt`. **Hard boundary: hooks cannot trigger
  slash commands / tool calls** -- only context injection or out-of-band
  channels. [10][29][30][43][44][45][69][70][71][72][96][119][120][160][161][162][180][181][182][183][184][223][224][225][235][236][244]
- **headless / Agent SDK**: `claude -p --output-format stream-json` NDJSON;
  Python `ClaudeSDKClient` / TS `query()`; partial-message streaming. The seam
  for EMBEDDING Claude in a kitten/panel vs scraping a pane.
  [31][76][97][123][186][209][232][253]
- **MCP** (stdio/HTTP): cleanest way to hand Claude typed kitty tools instead of
  raw `kitten @` bash. [187]
- **plugins**: bundle skills+hooks+statusline+commands+MCP as one unit.
  Constraint: plugin-defined subagents do NOT support
  hooks/mcpServers/permissionMode frontmatter -- those agents must live in
  `.claude/agents/`. [104][141][142]

---

## B. Prior art (real integrations -- corroborate before relying)

**The one real kitty+Claude integration:** `andrew-enns-tribe/kitty-plugin` --
a Claude Code plugin, `/kitty` skill for NL window control, ~8 helper scripts,
`ch_bg.py` watcher (theme rotation), F12 "AFK Command Center" (one window
controls all other Claude sessions, phone-usable). **Uses ONLY remote control.**
Does NOT touch graphics, panels, statusline JSON, or per-pane agent-state.
Resolves cwd via `lsof` because kitty's cached cwd is unreliable -- a real
gotcha to inherit. [25][73][124][163][188][207][226]

**Other kitty-native bits:** a "Kitten" community skill (thin SKILL.md over RC;
list/read/send/launch) [164][206]; TIAcode/GPTKitten (OpenAI-targeted kitten,
`--stdin-source` context capture; author flags it poor quality -- a clean Claude
reimplementation is a clear gap) [5][26]; prompt-navigation via regex marks on
the prompt glyphs (Kovid-endorsed, no kitten needed, but fragile -- relies on
glyphs, not OSC 133). [24][48][113][165][208]

**Agent-aware terminals (design references, NOT kitty):**
- **cmux** (manaflow-ai, ~10k stars) -- the most complete shipping harness:
  libghostty/Swift/macOS, daemon + socket API, read-screen + send + OSC
  9/99/777 notifications + `cmux hooks setup`, pane rings, sidebar
  (git branch/PR/cwd/ports), `cmux claude-teams` native splits. **Locked to
  Ghostty/macOS** -- the validated pattern a kitty equivalent should replicate
  in-process. [33][98][99][100][166][246]
- **teru** -- the closest design template: OSC 9999 agent self-declaration +
  MCP server (~19 tools: list_panes/read_output/send_input/broadcast/
  get_process_graph) over a unix socket + process/agent DAG. A kitty equivalent
  assembles from OSC 1337 + `kitten @` + a watcher-maintained graph; nobody
  shipped this. [228][229]
- **agent-tui** (PTY screenshot-and-replay); **pi-agent/pi-tui** (flicker-free
  agent TUI that already speaks kitty graphics -- closest agent-TUI-on-kitty
  foundation); **claude-notifications-go** (treats kitty as one of many
  click-to-focus backends, no programmable use). [211][247][245]

**tmux-centric multi-agent ecosystem (the saturated, fragile baseline):** amux,
tmai, claude-squad, claude_code_agent_farm (20+ agents), Tmux-Orchestrator,
ittybitty, ntm, ccmanager, agent-tmux-manager (atm -- hooks-over-socket done
right, the accuracy model to copy), overstory. Core fragility: `send-keys`
needs literal `-l` + a separate Enter; multi-subprocess races garble input at
4+ agents (~50%). [2][33][52][101][102][103][145][169][189][210][230]

**False leads to disambiguate:** `kitty-bridge` (an LLM proxy) and `spec-kitty`
(an SDD CLI) are NOT the kitty terminal. [14][75][125][248]

---

## C. Integration seams ranked for THIS harness

1. **`launch --type=background` daemon bound to the kitty instance** (inherits
   `KITTY_LISTEN_ON`). Home for a long-lived controller that (a) receives Claude
   http-hooks + statusline ticks, (b) drives `kitten @` back to windows matched
   by user-vars. Closes the loop hooks alone can't (hooks observe; daemon acts).
   [130][131][190][244][249]
2. **user-var routing** (`--match var:role=...`): tag each agent pane
   declaratively; address "the window running claude in /repo" or
   "env TEAMMATE=reviewer" instead of opaque indices -- the categorical win over
   tmux. [1][59][77][81][106][134][149][173][216][217][239]
3. **statusLine / subagentStatusLine -> kitty user-vars bridge**: a statusline
   script can `kitten @ set-user-vars` to mirror context%/cost/rate-limit into
   tab titles or a panel. [122][185][243][66]
4. **`terminalSequence` / Notification hook -> OSC 99**: native, TTY-free path
   for agent lifecycle -> kitty notifications/titles; click-to-focus the exact
   pane via `a=focus`. [29][44][119][72][184][205][222][237]
5. **watcher `on_cmd_startstop` + `on_set_user_var`**: agent-agnostic busy/idle
   detection and an in-band state channel, no polling. [32][133][196][238]
6. **Embed the Agent SDK in a kitten/panel** to render `stream-json` directly
   (vs scraping). Watch the BILLING change: as of 2026-06-15, `claude -p` / the
   Agent SDK bill against a SEPARATE credit pool, not interactive quota -- a real
   "scrape interactive pane vs drive headless" cost tradeoff.
   [76][123][186][209][232][233]

---

## D. Gaps nobody has filled (the build targets)

- **Inline graphics for agents is wholly unused.** Claude's own TUI CANNOT
  render inline images (issue #2266 closed; architectural: renderer strips
  OSC/APC, Bash stdout is captured-not-PTY, alt-screen repaint overwrites). So
  diffs-as-images, d2/mermaid diagrams, and spend charts must live in an
  OUT-OF-BAND kitty panel/overlay, not in the Claude pane. The graphics protocol
  is now portable (Ghostty/WezTerm/iTerm2/Konsole). [12][49][65][127][157][177][250]
- **A kitty-native multi-agent orchestrator**: per-agent windows keyed by
  user-var, watcher `on_close` auto-restart, a panel-kitten dashboard, graphics
  for diffs. The entire shipping ecosystem is tmux-based. [33][169][189][230]
- **kitty as a Claude agent-teams split backend.** Teams split-pane is hardcoded
  to tmux/iTerm2; kitty is not first-class. `launch --location vsplit` + RC over
  one socket sidesteps the tmux multi-subprocess send-keys race. Open Anthropic
  proposal **#26572 CustomPaneBackend** (JSON-RPC/NDJSON,
  `CLAUDE_PANE_BACKEND[_SOCKET]`) -- if adopted, that is the official seam to
  make kitty a backend; unimplemented today. [143][144][167][231]
- **No semantic boundary protocol between a TUI agent and the terminal.** TUI
  agents emit no OSC 133, so prompt-jump / last-output-capture / cwd-reporting
  silently break inside a Claude pane; everyone falls back to fragile glyph
  regex. A harness must synthesize this (hooks emitting OSC 1337 / OSC 7).
  [112][113][227]
- **Clickable file:line.** Claude doesn't emit OSC 8 on the paths it prints
  (issues #13008 / #27889); a kitty harness could post-process `transcript_path`
  or use `open_actions.conf`. [140][136][204]
- **A kitty-native MCP server wrapping `kitten @`** with typed tools +
  capability-scoped `remote_control_password` (e.g. allow get-text/ls, deny
  send-text). [187][37][82][214][252]
- **An out-of-band kitty panel fed by statusline JSON.** The statusline projects
  (ccstatusline, claude-powerline, oh-my-claude, starship-claude) are crowded
  but all text-only/portable; none exploit panel/graphics/OSC-99-buttons. [54][168]

---

## E. Caveats to verify against YOUR installed versions (2026-current)

- Agent teams are experimental, off by default (v2.1.32+):
  `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS` + `teammateMode`
  (in-process|tmux|auto). `context_window` token semantics changed v2.1.132.
  `COLUMNS/LINES` for statusline landed v2.1.153+. kitty panel on macOS needs
  0.42.0+. Verify with `claude --version` / `kitten --version`. [13][55]
- kitty has **no detached-session persistence** -- if kitty dies, all windows
  die; for remote/long-running agents you still want tmux/screen INSIDE kitty.
  The kitty trade is richer in-band control for lost persistence. [126]
- Claude Code keyboard-protocol detection ignores `KITTY_WINDOW_ID` when
  `TERM_PROGRAM` is set (#27868); historic `/terminal-setup` gap (#3853). Don't
  assume auto-detection works. [35]
- Notifications/graphics are swallowed under tmux without
  `set -g allow-passthrough on`; run Claude DIRECTLY in kitty for reliable OSC 8
  statusline links + notifications. [50][139][157]
- Claude's `/remote-control` (device-continuation feature) is UNRELATED to kitty
  `kitten @`. [51]
- `draw_tab` signature changed in kitty 0.33 (broke custom tab bars, #7242) --
  tab_bar.py is version-sensitive. [64]
- macOS specifics: the quick-access global hotkey routes through
  System Settings > Services; keyboard grabbing is unavailable; panel dock-mode
  does not reserve screen space on macOS. [117][234]

---

## My read: where I'd start, given your setup

You already have the two hardest-won pieces: a rich statusline that parses the
full Claude JSON, and a nix-managed hook/skill/workflow rig. That makes the
cheapest-to-highest-leverage path:

1. **statusLine -> kitty user-vars bridge (seam C3).** Smallest diff with real
   payoff: your existing statusline script also does `kitten @ set-user-vars`
   to stamp context%/cost/spend-trend/agent-name onto the window, so panes
   become addressable by state and tab titles carry the spend ledger. Pure
   reuse of code you already wrote.
2. **A long-lived `launch --type=background` controller (seam C1) + http hooks.**
   The piece the one existing kitty+Claude plugin lacks. It turns your hooks
   from observe-only into a closed loop (hook fires -> daemon drives
   `kitten @`), and it is where the out-of-band panel + orchestrator would live.
3. **Out-of-band graphics panel (gap D1).** This is the genuinely novel one --
   render diffs / d2 diagrams / a spend chart into a kitty panel beside the
   Claude pane, since Claude's own TUI provably can't. Plays directly to your
   d2 + statusline aesthetic.

Lower-priority but clean: a typed kitty MCP server (gap D6) so I drive kitty
through capability-scoped tools instead of raw `kitten @` bash; and synthesizing
OSC 133 boundaries (gap D4) so prompt-jump/cwd work inside Claude panes.

The strategic note from the research: the whole field is tmux-based and fragile
at 4+ agents; the kitty-native in-process path (watchers + RC over one socket +
user-vars) is both more robust AND unoccupied. The cost you accept is kitty's
lack of detached persistence (E2).

---

## Resolvable anchor sources

- kitty remote control: https://sw.kovidgoyal.net/kitty/remote-control/
- kitty kittens / watchers / graphics / panel: https://sw.kovidgoyal.net/kitty/
- Claude Code hooks: https://code.claude.com/docs/en/hooks
- Claude Code statusline / docs: https://code.claude.com/docs/en/
- andrew-enns-tribe/kitty-plugin: https://github.com/andrew-enns-tribe/kitty-plugin
- manaflow-ai/cmux: https://github.com/manaflow-ai/cmux
- CustomPaneBackend proposal: https://github.com/anthropics/claude-code/issues/26572

(The full per-claim `[N]` URL list is in the workflow transcript for this run.)
