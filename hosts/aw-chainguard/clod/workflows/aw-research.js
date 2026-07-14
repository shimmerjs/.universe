export const meta = {
  name: 'aw-research',
  description: '[fanout=6 passes=2 verify=3 breadth=web,code,docs codex=on intensity=5 subagents=custom|stock] Fan-out research with loop-until-dry passes, a cross-model codex search leg, and adversarial verification; informal word=value flags (long or short, anywhere in the prompt)',
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
  codex:     { short: 'x', type: 'str',  default: 'on', choices: ['on', 'off'], help: 'cross-model codex search leg in round 1 (live web search proven on 0.144.1)' },
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
// phase-0 budget guard: worst-case lifetime agent() spawns from the resolved
// flags, computed BEFORE the first spawn. The runtime kills a run at 1000
// lifetime agents; hitting that mid-run loses synthesis and the whole result
// (the 2026-07-07 aw-research incident), so an over-budget invocation aborts
// up front instead of dying at the finish line. Copied verbatim across the
// aw-*.js that can legally exceed the cap.
function worstCaseAgents(passes, fanout, votes, cap, overhead) {
  return passes * (fanout + cap * votes) + overhead
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

// name-plate: prefix every agent prompt with [<wf>:<leg>]. The plate is the
// ONLY channel that reaches external observers -- khudson's clod panel reads
// transcript heads for leg names and the run name; label: feeds only the
// in-app progress UI. Keep legs short ASCII (<=60 chars, no ] or "), so
// pathy legs use basenames. Copied verbatim across every aw-*.js (only WF
// differs).
const WF = 'aw-research'
const named = (leg, prompt, opts) => agent('[' + WF + ':' + leg + '] ' + prompt, { label: leg, ...opts })

const CLAIM   = { type: 'object', required: ['claims'], properties: { claims: { type: 'array', items: {
                  type: 'object', required: ['text'], properties: { text: { type: 'string' }, source: { type: 'string' } } } } } }
const VERDICT = { type: 'object', required: ['refuted'], properties: { refuted: { type: 'boolean' }, why: { type: 'string' } } }

// codex search leg (round 1 only): a different vendor's model AND retrieval
// stack searching the same question -- recall the same-model searchers miss.
// Live web search proven on codex 0.144.1 (the 0.142.x probe failed; revisit
// condition met). Claims PREPEND to round 1's pool and ride the same dedup +
// skeptic quorum; the verify cap gets +4 headroom so the extra leg cannot
// crowd Claude claims out. Dead leg (auth/exec error) stays distinguishable
// from "found nothing" via available=false.
const CODEX_CLAIMS = { type: 'object', required: ['available', 'claims', 'dropped'], properties: {
  available: { type: 'boolean' }, error: { type: 'string' }, dropped: { type: 'integer' },
  claims: { type: 'array', items: { type: 'object', required: ['text'], properties: {
    text: { type: 'string' }, source: { type: 'string' } } } } } }
let codexDown = false, codexDropped = 0
const codexLeg = flags.codex !== 'on' ? null : () => named('search:codex:1',
  'Cross-model search leg: relay codex, do NOT add claims of your own.\n' +
  '1. Write the research question below to a temp file (mktemp; a quoted bash heredoc is quoting-safe -- the question may contain $, backticks, or quotes), then run:\n' +
  '   codex exec -s read-only -c \'tools.web_search=true\' -o <mktemp-out> "Read <input-file>. Research that question with live web search. Return concrete factual claims, each on its own line with a source URL."\n' +
  '2. Read the output file. Drop claims with no source URL and count them in dropped; keep codex\'s wording in text.\n' +
  '3. Return available=true with the surviving claims. If codex exits nonzero or errors (a 401 after retry churn means auth expired), return available=false with the first error lines in error. Never fabricate claims.\n' +
  'Research question:\n' + prompt,
  { phase: 'Search', agentType: RESEARCHER, schema: CODEX_CLAIMS })
const CAP = VERIFY_CAP + (flags.codex === 'on' ? 4 : 0)

// overhead: the codex leg + the synthesize agent.
const AGENT_BUDGET = 900
const worst = worstCaseAgents(flags.passes, flags.fanout, flags.verify, CAP, (codexLeg ? 1 : 0) + 1)
if (worst > AGENT_BUDGET) {
  log('rejected: worst-case ' + worst + ' agents > ' + AGENT_BUDGET + ' (runtime lifetime cap is 1000) -- lower passes/fanout/verify/intensity')
  return { error: 'agent budget: worst case ' + worst + ' exceeds ' + AGENT_BUDGET,
           rejected: { passes: flags.passes, fanout: flags.fanout, verify: flags.verify, cap: CAP } }
}

const seen = new Set(), confirmed = [], unverified = []
let refuted = 0, failed = 0, overCap = 0
for (let round = 0; round < flags.passes; round++) {
  phase('Search')
  const thunks = Array.from({ length: flags.fanout }, (_, i) => {
    const mode = flags.breadth[i % flags.breadth.length]
    return () => named(`search:${mode}:${i + 1}`,
      `Round ${round + 1}, searcher ${i + 1} (${mode}). Research from an angle no other searcher would take: ${prompt}. Return concrete, sourced claims.`,
      { phase: 'Search', schema: CLAIM, agentType: RESEARCHER })
  })
  if (round === 0 && codexLeg) thunks.unshift(codexLeg)
  const results = (await parallel(thunks)).filter(Boolean)
  let found = []
  for (const r of results) {
    if (r.available === undefined) { found = found.concat(r.claims || []); continue }
    if (!r.available) { codexDown = true; log('codex search leg DOWN: ' + (r.error || 'no error text')) }
    else { codexDropped = r.dropped || 0; found = (r.claims || []).concat(found) }
  }
  if (round === 0 && codexLeg && !results.some(r => r.available !== undefined)) {
    codexDown = true
    log('codex search leg DOWN: agent died')
  }
  const fresh = found.filter(c => { const k = (c.text || '').slice(0, 80).toLowerCase()
                                    if (seen.has(k)) return false; seen.add(k); return true })
  log(`round ${round + 1}: ${found.length} claims, ${fresh.length} fresh`)
  if (!fresh.length) { log('dry round -- stopping early'); break }

  if (flags.verify === 0) { unverified.push(...fresh); continue }  // verify disabled: unverified, not laundered into confirmed
  phase('Verify')
  const { take, over } = capClaims(fresh, CAP)
  if (over.length) {
    log('verify cap: verified ' + take.length + '/' + fresh.length + ', ' + over.length + ' over cap -> UNVERIFIED')
    unverified.push(...over)
    overCap += over.length
  }
  const judged = await parallel(take.map(c => () =>
    parallel(Array.from({ length: flags.verify }, (_, k) => () =>
      named(`verify:${(c.text || '').replace(/[\]"\\]/g, '').slice(0, 24)}`, `Skeptic ${k + 1}: try to REFUTE this claim. Default refuted=true if you can't independently confirm it.\nClaim: ${c.text}\nSource: ${c.source || 'none'}`,
        { phase: 'Verify', schema: VERDICT, agentType: SKEPTIC })))
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
const report = await named('synthesize',
  `Synthesize a cited answer to: ${prompt}\nUse ONLY these ${basisNote}:\n` +
  basis.map((c, i) => `[${i + 1}] ${c.text} (${c.source || 'unsourced'})`).join('\n'),
  { phase: 'Synthesize', agentType: RESEARCHER })
return { question: prompt, flags, verified: confirmed.length, refuted, unverified: unverified.length, failed, overCap, codexDown, codexDropped, report }
