export const meta = {
  name: 'aw-research',
  description: '[fanout=6 passes=2 verify=3 breadth=web,code,docs intensity=5 subagents=custom|stock] Fan-out research with loop-until-dry passes and adversarial verification; informal word=value flags (long or short, anywhere in the prompt)',
  whenToUse: 'Deep multi-source research; tune fanout, passes, verify, breadth',
  phases: [{ title: 'Search' }, { title: 'Verify' }, { title: 'Synthesize' }],
}

// Flag specs: single source of truth for parseFlags AND the lint/cheatsheet
// extractors, which slice this literal textually (closing brace at col 0).
// Lives OUTSIDE meta: the Workflow runtime strips the meta export before
// running the body, so the body can only reach a plain const.
const FLAGS = {
  fanout:    { short: 'f', type: 'int',  default: 6, min: 1, max: 16, help: 'parallel searchers' },
  passes:    { short: 'p', type: 'int',  default: 2, min: 1, max: 6,  help: 'loop-until-dry rounds' },
  verify:    { short: 'v', type: 'int',  default: 3, min: 0, max: 5,  help: 'skeptics per claim (0 disables verification)' },
  breadth:   { short: 'b', type: 'list', default: ['web', 'code', 'docs'], help: 'search angles' },
  intensity: { short: 'i', type: 'int',  default: 5, min: 0, max: 10, help: 'one knob scaling unset fanout/verify/passes' },
  subagents: { short: 's', type: 'str',  default: 'custom', choices: ['custom', 'stock'], help: 'stock drops the custom agent types' },
}

// Examples:
//   research how does gopls index large modules
//   research fanout=8 passes=3 breadth=web,code how does X work

// flags: <word>=<value> or <short>=<value>, pulled from ANYWHERE in the prompt;
// remaining tokens (in order) are the prompt. unknown word=value stays verbatim.
// canonical parser -- copied byte-for-byte into every aw-*.js (no runtime imports).
function coerce(v, s) {
  if (s.type === 'int')  { let n = parseInt(v, 10); if (isNaN(n)) n = s.default;
                           if (s.min != null) n = Math.max(s.min, n);
                           if (s.max != null) n = Math.min(s.max, n); return n }
  if (s.type === 'list') { const _p = String(v).split(',').map(x => x.trim()).filter(Boolean); return _p.length ? _p : s.default }
  if (s.type === 'axes') { const p = String(v).split(',').map(x => x.trim()).filter(Boolean)
                           if (!p.length) return s.default
                           return (p.length === 1 && /^\d+$/.test(p[0])) ? { count: Math.max(1, parseInt(p[0], 10)) } : { list: p } }
  if (s.type === 'path') return String(v)
  return String(v)
}
function parseFlags(raw, spec) {
  const flags = {}, alias = {}
  for (const k in spec) { flags[k] = spec[k].default; if (spec[k].short) alias[spec[k].short] = k }
  const set = new Set(), keep = [], errors = []
  const text = (typeof raw === 'string' ? raw : (raw && raw.prompt) || '').trim()
  const toks = text.length ? text.split(/\s+/) : []
  for (const t of toks) {
    const m = /^([A-Za-z][A-Za-z0-9_-]*)=(.*)$/.exec(t)
    const key = m && (m[1] in spec ? m[1] : (m[1] in alias ? alias[m[1]] : null))
    if (key && (m[2][0] === "'" || m[2][0] === '"'))  // tripwire: quotes do not survive the whitespace tokenizer
      errors.push(key + ': quoted value truncated by the whitespace tokenizer: ' + t)
    else if (key) { flags[key] = coerce(m[2], spec[key]); set.add(key) }  // known long/short, anywhere
    else keep.push(t)                                                // unknown word=value or prose -> prompt
  }
  return { flags, prompt: keep.join(' '), set, errors }
}

const { flags, prompt, set, errors } = parseFlags(args, FLAGS)
// phase-0 gate: a tokenizer-truncated flag value aborts before ANY agent spawns.
if (errors.length) {
  for (const e of errors) log('rejected: ' + e)
  return { error: 'flag value truncated by the whitespace tokenizer', rejected: errors }
}
// intensity: one 0-10 knob. Applied ONLY when the user passes it, and only to
// knobs they did not set explicitly, so the tuned defaults stand otherwise.
// cap: the per-round ceiling on claims entering the verify fan-out.
function fromIntensity(i) {
  i = Math.max(0, Math.min(10, i))
  return {
    fanout: Math.max(1, Math.round(1 + i * 1.5)),
    votes:  i <= 1 ? 1 : i <= 4 ? 2 : i <= 7 ? 3 : i <= 9 ? 4 : 5,
    passes: i === 0 ? 1 : Math.max(1, Math.round(i / 3)),
    cap:    Math.max(4, 4 * (i + 1)),
  }
}
function applyIntensity(flags, set) {
  if (!set.has('intensity')) return flags
  const k = fromIntensity(flags.intensity)
  // map onto whichever of this workflow's knobs exist; only override the unset ones.
  for (const [flag, val] of [['verify', k.votes], ['fanout', k.fanout], ['passes', k.passes]])
    if (!set.has(flag)) flags[flag] = val
  return flags
}
applyIntensity(flags, set)
const DEFAULT_INTENSITY = 5
const VERIFY_CAP = fromIntensity(set.has('intensity') ? flags.intensity : DEFAULT_INTENSITY).cap
// loud cap split: callers log take/over and tag over UNVERIFIED -- never a silent slice.
function capClaims(list, cap) {
  return { take: list.slice(0, cap), over: list.slice(cap) }
}
// sub-quorum -> UNVERIFIED (not kept, not dropped); ties -> REFUTED (refuted >= confirmed),
// per the workflows/CLAUDE.md reducer rule; judged over actual votes returned.
function tallyVotes(vv, total) {
  if (vv.length < Math.ceil(total / 2)) return 'UNVERIFIED'
  return vv.filter(x => x.refuted).length >= vv.length / 2 ? 'REFUTED' : 'CONFIRMED'
}
const stock = flags.subagents === 'stock'
if (!prompt) { log('no question after the flags -- nothing to research'); return { error: 'no question given', rejected: '' } }
log(`research: fanout=${flags.fanout} passes=${flags.passes} verify=${flags.verify} breadth=${flags.breadth.join('+')}`)

// Paired subagents. These resolve from the agent registry, which is frozen at
// SESSION START -- they only work in a fresh session after the nix rebuild that
// deploys clod/agents/{researcher,skeptic}.md to ~/.claude/agents/. Set either to
// undefined to fall back to the generic workflow subagent.
const RESEARCHER = stock ? undefined : 'researcher'
const SKEPTIC    = stock ? undefined : 'skeptic'

const CLAIM   = { type: 'object', required: ['claims'], properties: { claims: { type: 'array', items: {
                  type: 'object', required: ['text'], properties: { text: { type: 'string' }, source: { type: 'string' } } } } } }
const VERDICT = { type: 'object', required: ['refuted'], properties: { refuted: { type: 'boolean' }, why: { type: 'string' } } }

const seen = new Set(), confirmed = [], unverified = []
let refuted = 0, failed = 0, overCap = 0
for (let round = 0; round < flags.passes; round++) {
  phase('Search')
  const found = (await parallel(Array.from({ length: flags.fanout }, (_, i) => {
    const mode = flags.breadth[i % flags.breadth.length]
    return () => agent(
      `Round ${round + 1}, searcher ${i + 1} (${mode}). Research from an angle no other searcher would take: ${prompt}. Return concrete, sourced claims.`,
      { label: `search:${mode}:${i + 1}`, phase: 'Search', schema: CLAIM, agentType: RESEARCHER })
  }))).filter(Boolean).flatMap(r => r.claims || [])
  const fresh = found.filter(c => { const k = (c.text || '').slice(0, 80).toLowerCase()
                                    if (seen.has(k)) return false; seen.add(k); return true })
  log(`round ${round + 1}: ${found.length} claims, ${fresh.length} fresh`)
  if (!fresh.length) { log('dry round -- stopping early'); break }

  if (flags.verify === 0) { unverified.push(...fresh); continue }  // verify disabled: unverified, not laundered into confirmed
  phase('Verify')
  const { take, over } = capClaims(fresh, VERIFY_CAP)
  if (over.length) {
    log('verify cap: verified ' + take.length + '/' + fresh.length + ', ' + over.length + ' over cap -> UNVERIFIED')
    unverified.push(...over)
    overCap += over.length
  }
  const judged = await parallel(take.map(c => () =>
    parallel(Array.from({ length: flags.verify }, (_, k) => () =>
      agent(`Skeptic ${k + 1}: try to REFUTE this claim. Default refuted=true if you can't independently confirm it.\nClaim: ${c.text}\nSource: ${c.source || 'none'}`,
        { label: `verify:${(c.text || '').slice(0, 24)}`, phase: 'Verify', schema: VERDICT, agentType: SKEPTIC })))
      .then(votes => {
        const vv = votes.filter(Boolean)
        return { c, verdict: tallyVotes(vv, flags.verify), failed: flags.verify - vv.length }
      })))
  for (const j of judged.filter(Boolean)) {
    if (j.verdict === 'CONFIRMED') confirmed.push(j.c)
    else if (j.verdict === 'UNVERIFIED') unverified.push(j.c)
    else refuted++
    failed += j.failed
  }
}
phase('Synthesize')
// With no confirmed claims, synthesize from `unverified` but label the actual
// cause honestly: verify=0 (verification disabled) vs claims left over the
// per-round cap -- the two read very differently downstream.
const basis = confirmed.length ? confirmed : unverified
const basisNote = confirmed.length
  ? 'verified claims (each survived refutation)'
  : flags.verify === 0
    ? 'UNVERIFIED claims -- verification was disabled (verify=0); flag the whole answer as unverified'
    : `UNVERIFIED claims -- none confirmed${overCap ? ` (${overCap} never verified, over the per-round cap)` : ''}; flag the whole answer as unverified`
const report = await agent(
  `Synthesize a cited answer to: ${prompt}\nUse ONLY these ${basisNote}:\n` +
  basis.map((c, i) => `[${i + 1}] ${c.text} (${c.source || 'unsourced'})`).join('\n'),
  { label: 'synthesize', phase: 'Synthesize', agentType: RESEARCHER })
return { question: prompt, flags, verified: confirmed.length, refuted, unverified: unverified.length, failed, overCap, report }
