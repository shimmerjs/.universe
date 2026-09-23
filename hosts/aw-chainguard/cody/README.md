# cody

codex (OpenAI's CLI) wired in as clod's second opinion. "cody" is what I call him.

every Claude agent on this machine runs the same pinned model, so a fan-out multiplies eyes but not perspectives -- ten reviewers share one set of blind spots. cody is the different-vendor dissent: the `/codex-consult` skill for one-off consults, and a leg in all six aw-* workflows. finder legs feed the skeptic quorum, search legs run with live web search, and aw-implement only brings him in at review time -- never while code is being written.

## layout

```
cody/
  default.nix      programs.codex + the clod-side wiring
  profiles.nix     the four config layers (this is the whole settings surface)
  overlays.nix     darwin-layer codex package overlay
  AGENTS.md        the consultant contract cody operates under
  hooks.nix        the hook estate (delivered as a managed config layer)
  agents/          agent roles, one .toml each
  codex-consult/   the skill that teaches clod when and how to consult him
```

`default.nix` also contributes the codex-consult skill and the `Bash(codex ...)` allowlist entries to `programs.claude-code`. module contributions merge (attrs deep, lists concatenated), so clod stays ignorant of cody and the host imports each directly.

the package comes from `overlays.nix`: codex fresh off nixpkgs master (the `nixpkgs-claude` input), because the server gates new models on CLI version -- a stale codex can't reach the current model at all. that file is darwin-level rather than home-manager: hm modules can't touch `nixpkgs.overlays` under `useGlobalPkgs`. the claude-code half of the same input is `../clod/overlays.nix`.

## the config posture, and why it looks odd

**`~/.codex/config.toml` is not managed.** that is the whole design, not an omission.

codex rewrites that file at runtime from about twenty call sites -- `/model`, theme, keymap, folder trust, notice dismissals, `codex mcp add`, `codex features enable` -- and its writer *dereferences the symlink chain* and creates its temp file next to the final target (`core/src/config/edit.rs:749` -> `utils/path-utils/src/lib.rs:47`). point that at the store and every one of those saves is a permanent EACCES. there is no fallback to replacing the symlink; upstream has a test asserting the follow-the-symlink behaviour is intentional. so nix leaves it alone and interactive codex works whole.

everything nix wants to say lives in **profile layers** instead: `$CODEX_HOME/<name>.config.toml`, each a complete config selected with `codex exec -p <name>`. a profile file is a full User layer at precedence 21, sitting just above the base user config, and it carries the entire config schema -- not a restricted subset.

| profile    | model        | effort | web search | used by |
|------------|--------------|--------|------------|---------|
| `review`   | gpt-6-astra  | max    | cached     | aw-review, aw-audit, aw-design-review, aw-implement |
| `research` | gpt-6-astra  | max    | live       | aw-research, aw-prior-art |
| `deep`     | gpt-6-astra  | ultra  | live       | standalone hard consults, by hand |
| `triage`   | gpt-5.6-sol  | low    | cached     | cheap sanity checks |

`ultra` is max reasoning **plus automatic task delegation** -- codex spawns its own subagents. worth it for a standalone consult, wasteful inside a leg clod already fanned out, so it is its own profile rather than the default.

three things follow from all this, and they bite if forgotten:

- **there is no default profile.** no env var, no config key, and the legacy top-level `profile =` selector is fatal in 0.153.4. every call site names its profile. a call without `-p` runs on the account default at no pinned effort.
- **`-p` goes after the subcommand.** `codex exec -p review ...`, not `codex -p review exec ...`. both parse; only the first matches the `Bash(codex exec:*)` allowlist entry.
- **a profile is not write-free either.** under `-p` it becomes the active user layer and the TUI's persistence paths retarget onto it. that costs nothing here only because the callers that pass `-p` are all non-interactive, and `codex exec` never persists config. don't start passing `-p` to the TUI.

## agent roles

`agents/*.toml` deploy to `~/.codex/agents/nix/` and are auto-discovered -- no config.toml entry and no `-p` needed, which makes them the one part of the harness that also applies to interactive codex. they mirror clod's five subagents and shape the children `deep`'s ultra effort spawns.

two constraints the check pins, because both fail silently:

- the `.toml` files must be **real regular files**. the spawn-time read is `O_NOFOLLOW`, so a symlinked role file loads clean at startup and only fails when something tries to use it. hence the *subdirectory* symlink: `~/.codex/agents` stays a real directory, and the store holds the files.
- the file's `name` wins over its filename. a mismatch registers the role under a name nothing predicts.

a role may only *reduce* capability. it can pin model, effort, verbosity, personality, service tier and developer instructions, and switch six coarse features off -- but `sandbox_mode`, `approval_policy` and `mcp_servers` in a role file are parsed, validated, and then discarded. there is no per-role sandbox.

## skills

`deslop` and `handoff` are shared verbatim with clod: codex's SKILL.md parser reads the same `name`/`description` frontmatter and ignores the Claude-only keys. `go` is deliberately not shared -- it documents clod's format/build/vet hook estate, which codex has no counterpart for.

home-manager deploys each as a symlinked *directory*. that matters: codex's discovery walk skips symlinked non-directories with no warning, so a skill deployed as a symlinked file is simply invisible.

`skills.bundled.enabled = false` in every profile stops codex unpacking its six sample skills into `~/.codex/skills/.system` on each startup, and hands their share of the catalog token budget (2% of context by default) back.

## hooks

the clod hook estate runs under codex too. same five guards, same binaries:

| event | matcher | hook |
|-------|---------|------|
| PreToolUse | `Bash\|apply_patch` | fancypants -- ASCII only, no banner comments |
| PostToolUse | `apply_patch` | gofmt syntax gate + goimports + queue for the Stop pass |
| PostToolUse | `Bash` | sweep stray `go build` binaries out of the tree |
| Stop | - | gocheck -- `go build` + `go vet` the packages touched this turn |
| SubagentStop | - | gocheck, same gate on subagent ends |

codex's engine is close enough to Claude's that the binaries are literally shared, not ported: same event names, one JSON object on stdin, exit 2 + stderr blocks, `decision: "block"` + `reason` on Stop forces a continuation. verified live end to end -- a commit message with an em dash comes back `hook: PreToolUse Blocked` with fancypants' own message. `fancypants` and `go-fmt-hook` grew a second payload shape for codex's `apply_patch` (a whole patch envelope in `tool_input.command` instead of a `file_path`), and since `apply_patch` never appears in Claude, the tool name alone selects the shape -- no flavor flag, one binary for both harnesses. `Write` and `Edit` are accepted as matcher aliases for `apply_patch`, but the payload always says `apply_patch`.

**the trust problem, and how nix sidesteps it.** codex refuses to run a hook from a user-owned source unless `hooks.state."<key>".trusted_hash` equals a hash it recomputes from the hook's normalized definition. nothing warns when it is missing: the hook is discovered, listed, and silently skipped. but a hook from a **managed** config layer is trust-exempt -- `is_managed` short-circuits the hash check outright.

so the estate does not ship as `~/.codex/hooks.json` at all. it ships as a `[hooks]` table in the `com.openai.codex` CFPreferences domain, which codex reads as a managed layer. that domain is the plain per-user one -- no MDM, no root, no configuration profile -- so a `defaults write` in a home-manager activation step is the whole delivery mechanism. no hash to mint, nothing to keep in sync with the hook binaries, no per-invocation flag, and `~/.codex/config.toml` is never touched.

the earlier design injected `-c 'hooks.state={...}'` from a wrapped binary, with the hash computed in nix. it worked, and it was verified firing -- but it is the wrong shape for daily driving. any `-c` override forces the TUI back to an in-process app-server, and if a session ever attaches to a shared one (`--remote`, `codex agents`) the client's `-c` is filtered to a typed `ConfigOverrides` struct on the way across the wire; `hooks` is not in it, so trust would silently stop arriving. hook discovery, trust, and execution all resolve from whichever process serves the session, and a per-user CFPreferences layer is read by that process whatever the topology. managed delivery survives the change; `-c` does not.

**keep the payload to `[hooks]` only.** that layer is `LegacyManagedConfigTomlFromMdm` at precedence 50 -- above the user's own config.toml (20) and above `-c` (30). anything put there becomes unoverridable, which is right for a guardrail and wrong for everything else. the flake check fails the build on any other top-level key.

the payload also lands at `~/.codex/nix-managed-hooks.toml`. codex does not read it there; it is the exact bytes activation encodes, so the deployed hook set is answerable with `cat` rather than `defaults read | base64 -d`, and the check validates the artifact that actually ships.

do **not** reach for `--dangerously-bypass-hook-trust` as a shortcut. measured: under that flag a `SessionStart` hook in a repo's own `.codex/hooks.json` executes. it needs the repo trusted first, but every repo you work in is trusted by definition, so it hands repo-controlled config a shell on session start.

`aw-scriptpath-gate` has no codex counterpart -- it guards Claude's Workflow tool, which codex does not have.

## what is deliberately absent

- **execpolicy `.rules`.** `programs.codex.rules` deploys files codex never reads: discovery filters on `is_file()`, which does not traverse a symlink. a symlinked `rules/` *directory* does work, but then codex can't append to `default.rules` and the "always allow this command" action breaks. and an `allow` rule is not "allow inside the sandbox" -- it runs the command with the sandbox *bypassed*, while the unmatched-command heuristic already approves non-dangerous commands without prompting. so allow rules trade prompts for unsandboxed processes. nothing here is worth that.
- **an LSP block.** codex has no LSP integration of any kind; `rg -i '\blsp\b'` over the source hits only `Cargo.lock`.
- **a statusline.** `tui.status_line` is a list of built-in item ids. there is no command form, no refresh interval, and no subagent row renderer, so clod's `statusline.sh` and its Go subagent renderer have no port.
- **spinner verbs.** the header string is the hardcoded literal `"Working"`. the only knob is `tui.animations` on/off.
- **memories / goals.** left at codex's defaults (memories off, goals on) by explicit choice.

## the check

`nix build .#checks.aarch64-darwin.cody-config-aw-chainguard` validates the *rendered* `~/.codex` against the *pinned* codex source: profile keys against the CLI's own `config.schema.json` (`additionalProperties: false`, the hermetic stand-in for `codex exec --strict-config`, which needs auth and the network), role files for real-file-ness and required fields, every model/effort pair against the pinned catalog, skills for directory-ness, the absence of a nix-owned `config.toml`, and the managed hook payload -- `[hooks]`-only, every command executable, every timeout explicit, and no `hooks.json` shadowing it. the legs are mutation-tested.

`nix flake check` does not work here -- it dies on `nixosConfigurations.snake` for unrelated reasons. build the named check.

## operating notes

- auth is the one mutable dependency. login state lives in `~/.codex/auth.json` and expires outside nix's control. expired auth does not fail clean: ~30s of retry churn, then a bare 401. `codex doctor` reports auth, the resolved model, and the config.toml parse result in one pass -- note it rejects `-p`, so it shows the base config.
- max/ultra on the flagship spends wall-clock. a 20-minute consult that reads a million tokens of repo is normal operation, not a hang.
- an in-repo `.codex/config.toml` in a trusted repo outranks these profiles (precedence 25 vs 21), and it is not denylisted from setting `sandbox_mode` or `approval_policy`. the only layer that could pin posture unconditionally is `/etc/codex/requirements.toml`, which this harness does not use.
- there is no shared app-server daemon, and that is fine. every `codex` invocation runs its app-server in-process; `codex exec` -- the path every aw-* fan-out takes -- has no daemon code at all, and the supported ways to start one (`codex app-server daemon start`, `codex remote-control start`) both need the installer-managed binary at `$CODEX_HOME/packages/standalone/current/codex`, which a nix install does not have. measured at 20 concurrent sessions: ~700 MB across 20 processes embedded versus ~178 MB in one daemon, so ~520 MB on a 36 GB box, and `thread/start` latency actually gets WORSE on a shared daemon at that concurrency (370-913 ms vs 180-190 ms) because thread creation serializes on one MessageProcessor. a daemon also does not dedupe MCP stdio children -- those are per-thread by construction. doctor's "ephemeral mode" is a settings.json existence check, not a mode you are in.
- cody's output is a claim, not a verdict. the skill and every workflow leg ground his citations before relaying them; do the same when consulting by hand.
