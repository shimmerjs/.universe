export const meta = {
  name: 'aw-research',
  description: '[fanout=6 passes=2 verify=3 breadth=web,code,docs intensity=5 subagents=custom|stock] Fan-out research with loop-until-dry passes and adversarial verification; informal word=value flags (long or short, anywhere in the prompt)',
  whenToUse: 'Deep multi-source research; tune fanout, passes, verify, breadth',
  phases: [{ title: 'Search' }, { title: 'Verify' }, { title: 'Synthesize' }],
  flags: {
    fanout:    { short: 'f', type: 'int',  default: 6, min: 1, max: 16, help: 'parallel searchers' },
    passes:    { short: 'p', type: 'int',  default: 2, min: 1, max: 6,  help: 'loop-until-dry rounds' },
    verify:    { short: 'v', type: 'int',  default: 3, min: 0, max: 5,  help: 'skeptics per claim (0 disables verification)' },
    breadth:   { short: 'b', type: 'list', default: ['web', 'code', 'docs'], help: 'search angles' },
    intensity: { short: 'i', type: 'int',  default: 5, min: 0, max: 10, help: 'one knob scaling unset fanout/verify/passes' },
    subagents: { short: 's', type: 'str',  default: 'custom', choices: ['custom', 'stock'], help: 'stock drops the custom agent types' },
  },
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
  return String(v)
}
function parseFlags(raw, spec) {
  const flags = {}, alias = {}
  for (const k in spec) { flags[k] = spec[k].default; if (spec[k].short) alias[spec[k].short] = k }
  const set = new Set(), keep = []
  const text = (typeof raw === 'string' ? raw : (raw && raw.prompt) || '').trim()
  const toks = text.length ? text.split(/\s+/) : []
  for (const t of toks) {
    const m = /^([A-Za-z][A-Za-z0-9_-]*)=(.*)$/.exec(t)
    const key = m && (m[1] in spec ? m[1] : (m[1] in alias ? alias[m[1]] : null))
    if (key) { flags[key] = coerce(m[2], spec[key]); set.add(key) }  // known long/short, anywhere
    else keep.push(t)                                                // unknown word=value or prose -> prompt
  }
  return { flags, prompt: keep.join(' '), set }
}

const { flags, prompt, set } = parseFlags(args, meta.flags)
// intensity: one 0-10 knob. Applied ONLY when the user passes it, and only to
// knobs they did not set explicitly, so the tuned defaults stand otherwise.
const fromIntensity = (i) => { i = Math.max(0, Math.min(10, i)); return {
  fanout: Math.max(1, Math.round(1 + i * 1.5)),
  votes:  i <= 1 ? 1 : i <= 4 ? 2 : i <= 7 ? 3 : i <= 9 ? 4 : 5,
  passes: i === 0 ? 1 : Math.max(1, Math.round(i / 3)),
} }
if (set.has('intensity')) {
  const k = fromIntensity(flags.intensity)
  // map onto whichever of this workflow's knobs exist; only override the unset ones.
  for (const [flag, val] of [['verify', k.votes], ['fanout', k.fanout], ['passes', k.passes]])
    if (!set.has(flag)) flags[flag] = val
}
const stock = flags.subagents === 'stock'
if (!prompt) { log('no question after the flags -- nothing to research'); return }
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
  const quorum = Math.ceil(flags.verify / 2)
  const judged = await parallel(fresh.map(c => () =>
    parallel(Array.from({ length: flags.verify }, (_, k) => () =>
      agent(`Skeptic ${k + 1}: try to REFUTE this claim. Default refuted=true if you can't independently confirm it.\nClaim: ${c.text}\nSource: ${c.source || 'none'}`,
        { label: `verify:${(c.text || '').slice(0, 24)}`, phase: 'Verify', schema: VERDICT, agentType: SKEPTIC })))
      .then(votes => {
        const vv = votes.filter(Boolean)
        if (vv.length < quorum) return { c, verdict: 'UNVERIFIED' }   // sub-quorum: not kept, not dropped
        // ties -> REFUTED (refuted >= confirmed), per the workflows/CLAUDE.md reducer rule; over actual votes returned
        return { c, verdict: vv.filter(x => x.refuted).length >= vv.length / 2 ? 'REFUTED' : 'CONFIRMED' }
      })))
  for (const j of judged.filter(Boolean)) {
    if (j.verdict === 'CONFIRMED') confirmed.push(j.c)
    else if (j.verdict === 'UNVERIFIED') unverified.push(j.c)
  }
}
phase('Synthesize')
// verify=0 disables verification, so claims land in `unverified`; synthesize from
// those but label the whole answer unverified. Otherwise synthesize confirmed-only.
const basis = confirmed.length ? confirmed : unverified
const basisNote = confirmed.length
  ? 'verified claims (each survived refutation)'
  : 'UNVERIFIED claims -- verification was disabled (verify=0); flag the whole answer as unverified'
const report = await agent(
  `Synthesize a cited answer to: ${prompt}\nUse ONLY these ${basisNote}:\n` +
  basis.map((c, i) => `[${i + 1}] ${c.text} (${c.source || 'unsourced'})`).join('\n'),
  { label: 'synthesize', phase: 'Synthesize', agentType: RESEARCHER })
return { question: prompt, flags, verified: confirmed.length, unverified: unverified.length, report }
