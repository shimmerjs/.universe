export const meta = {
  name: 'research',
  description: 'Fan-out research with loop-until-dry passes and adversarial verification; informal word=value flags',
  whenToUse: 'Deep multi-source research; tune fanout, passes, verify, breadth',
  phases: [{ title: 'Search' }, { title: 'Verify' }, { title: 'Synthesize' }],
}

// Examples:
//   research how does gopls index large modules
//   research fanout=8 passes=3 breadth=web,code how does X work

// ── informal flags: <word>=<value> (no spaces), space-separated, up front.
//    prompt begins at the first token that isn't a known <word>=<value>. ──
function coerce(v, s) {
  if (s.type === 'int')  { let n = parseInt(v, 10); if (isNaN(n)) n = s.default;
                           if (s.min != null) n = Math.max(s.min, n);
                           if (s.max != null) n = Math.min(s.max, n); return n }
  if (s.type === 'list') { const _p = String(v).split(',').map(x => x.trim()).filter(Boolean); return _p.length ? _p : s.default }
  return String(v)
}
function parseFlags(raw, spec) {
  const flags = {}; for (const k in spec) flags[k] = spec[k].default
  const text = (typeof raw === 'string' ? raw : (raw && raw.prompt) || '').trim()
  const toks = text.length ? text.split(/\s+/) : []
  let i = 0
  for (; i < toks.length; i++) {
    const m = /^([A-Za-z][A-Za-z0-9_-]*)=(.*)$/.exec(toks[i])  // word=value, no spaces
    if (!m || !(m[1] in spec)) break                          // first non-flag token -> prompt starts here
    flags[m[1]] = coerce(m[2], spec[m[1]])
  }
  return { flags, prompt: toks.slice(i).join(' ') }
}

const { flags, prompt } = parseFlags(args, {
  fanout:  { type: 'int',  default: 6, min: 1, max: 16 },        // parallel searchers
  passes:  { type: 'int',  default: 2, min: 1, max: 6  },        // loop-until-dry rounds
  verify:  { type: 'int',  default: 3, min: 0, max: 5  },        // skeptics per claim
  breadth: { type: 'list', default: ['web', 'code', 'docs'] },   // search angles (default ALL THREE)
})
if (!prompt) { log('no question after the flags -- nothing to research'); return }
log(`research: fanout=${flags.fanout} passes=${flags.passes} verify=${flags.verify} breadth=${flags.breadth.join('+')}`)

// Paired subagents. These resolve from the agent registry, which is frozen at
// SESSION START -- they only work in a fresh session after the nix rebuild that
// deploys clod/agents/{researcher,skeptic}.md to ~/.claude/agents/. Set either to
// undefined to fall back to the generic workflow subagent.
const RESEARCHER = 'researcher'
const SKEPTIC    = 'skeptic'

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

  if (flags.verify === 0) { confirmed.push(...fresh); continue }
  phase('Verify')
  const quorum = Math.ceil(flags.verify / 2)
  const judged = await parallel(fresh.map(c => () =>
    parallel(Array.from({ length: flags.verify }, (_, k) => () =>
      agent(`Skeptic ${k + 1}: try to REFUTE this claim. Default refuted=true if you can't independently confirm it.\nClaim: ${c.text}\nSource: ${c.source || 'none'}`,
        { label: `verify:${(c.text || '').slice(0, 24)}`, phase: 'Verify', schema: VERDICT, agentType: SKEPTIC })))
      .then(votes => {
        const vv = votes.filter(Boolean)
        if (vv.length < quorum) return { c, verdict: 'UNVERIFIED' }   // sub-quorum: not kept, not dropped
        return { c, verdict: vv.filter(x => x.refuted).length > flags.verify / 2 ? 'REFUTED' : 'CONFIRMED' }
      })))
  for (const j of judged.filter(Boolean)) {
    if (j.verdict === 'CONFIRMED') confirmed.push(j.c)
    else if (j.verdict === 'UNVERIFIED') unverified.push(j.c)
  }
}
phase('Synthesize')
const report = await agent(
  `Synthesize a cited answer to: ${prompt}\nUse ONLY these verified claims:\n` +
  confirmed.map((c, i) => `[${i + 1}] ${c.text} (${c.source || 'unsourced'})`).join('\n'),
  { label: 'synthesize', phase: 'Synthesize', agentType: RESEARCHER })
return { question: prompt, flags, verified: confirmed.length, unverified: unverified.length, report }
