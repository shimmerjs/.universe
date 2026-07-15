export const meta = {
  name: 'aw-prior-art',
  description: '[areas=5 verify-scope=load-bearing votes=3 codex=on intensity=5 subagents=custom|stock] Fan out deep-dives over prior-art sources (local/dep/web) plus a cross-model codex search leg, refute-verify in-scope claims with a skeptic quorum, synthesize a cited report that labels verified vs unverified claims plus a corrections section. word=value flags (long or short, anywhere in the prompt); the prompt is the question.',
  whenToUse: 'Researching how others solve X; tune areas, verify-scope',
  phases: [{ title: 'Plan' }, { title: 'Dig' }, { title: 'Verify' }, { title: 'Synthesize' }],
}

// Flag specs: single source of truth for parseFlags AND the lint/cheatsheet
// extractors, which slice this literal textually (closing brace at col 0).
// Lives OUTSIDE meta: the Workflow runtime strips the meta export before
// running the body, so the body can only reach a plain const.
const FLAGS = {
  areas:          { short: 'a', type: 'axes', default: { count: 5 }, help: 'investigation areas, or N to auto-derive' },
  'verify-scope': { short: 'c', type: 'str',  default: 'load-bearing', choices: ['all', 'load-bearing', 'none'], help: 'which claims get refuted' },
  votes:          { short: 'v', type: 'int',  default: 3, min: 1, max: 5, help: 'skeptics per in-scope claim' },
  codex:          { short: 'x', type: 'str',  default: 'on', choices: ['on', 'off'], help: 'cross-model codex search leg in the dig fan-out (live web search)' },
  intensity:      { short: 'i', type: 'int',  default: 5, min: 0, max: 10, help: 'one knob scaling the unset area count' },
  subagents:      { short: 's', type: 'str',  default: 'custom', choices: ['custom', 'stock'], help: 'stock drops the custom agent types' },
}

// Examples:
//   prior-art how do build systems model remote CAS GC
//   prior-art areas=bazel,buck2,nix verify-scope=load-bearing hermetic go test

// flags: <word>=<value> or <short>=<value>, pulled from ANYWHERE in the prompt;
// remaining tokens (in order) are the research question. unknown word=value stays verbatim.
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
// cap: the ceiling on claims entering the verify fan-out.
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
  // intensity scales the skeptic quorum and the investigation-area count.
  if (!set.has('votes')) flags.votes = k.votes
  if (!set.has('areas') && flags.areas && flags.areas.count != null) flags.areas = { count: k.fanout }
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
// phase-0 budget guard: worst-case lifetime agent() spawns from the resolved
// flags, computed BEFORE the first spawn. The runtime kills a run at 1000
// lifetime agents; hitting that mid-run loses synthesis and the whole result
// (the 2026-07-07 aw-research incident), so an over-budget invocation aborts
// up front instead of dying at the finish line. Copied verbatim across the
// aw-*.js that can legally exceed the cap.
function worstCaseAgents(passes, fanout, votes, cap, overhead) {
  return passes * (fanout + cap * votes) + overhead
}
// runtime budget guard: the phase-0 worst case is static, but real spend is
// not. With a run budget set (budget.total), input-driven growth points stop
// once budget.remaining() dips under RESERVE of the total, keeping the tail
// for the synthesize stage (which must ALWAYS run). No budget set ->
// remaining() is Infinity per the loop-until-budget contract, so the guard is
// inert and the static ceilings above govern alone. Copied verbatim across
// the aw-*.js with input-driven growth points.
const BUDGET_RESERVE = 0.2
function budgetLow(b, reserve) {
  return !!(b && b.total) && b.remaining() < b.total * reserve
}
const stock = flags.subagents === 'stock'

const RESEARCHER = stock ? undefined : 'researcher'
const SKEPTIC = stock ? undefined : 'skeptic'

// name-plate: prefix every agent prompt with [<wf>:<leg>]. The plate is the
// ONLY channel that reaches external observers -- khudson's clod panel reads
// transcript heads for leg names and the run name; label: feeds only the
// in-app progress UI. Keep legs short ASCII (<=60 chars, no ] or "), so
// pathy legs use basenames. Copied verbatim across every aw-*.js (only WF
// differs).
const WF = 'aw-prior-art'
const named = (leg, prompt, opts) => agent('[' + WF + ':' + leg + '] ' + prompt, { label: leg, ...opts })

if (!prompt) { log('no research question given after the flags'); return { error: 'no question given', rejected: '' } }

// area count is pre-spawn knowable either way (axes count is unclamped, so an
// areas=999 invocation is legal without this). verify-scope=none spawns zero
// verifiers, so the votes term drops. overhead: plan + codex leg + synthesize.
const CAP = VERIFY_CAP + (flags.codex === 'on' ? 4 : 0)
const areaCount = flags.areas.list ? flags.areas.list.length : flags.areas.count
const AGENT_BUDGET = 900
const worst = worstCaseAgents(1, areaCount, flags['verify-scope'] === 'none' ? 0 : flags.votes, CAP, (flags.codex === 'on' ? 1 : 0) + 2)
if (worst > AGENT_BUDGET) {
  log('rejected: worst-case ' + worst + ' agents > ' + AGENT_BUDGET + ' (runtime lifetime cap is 1000) -- lower areas/votes/intensity')
  return { error: 'agent budget: worst case ' + worst + ' exceeds ' + AGENT_BUDGET,
           rejected: { areas: areaCount, votes: flags.votes, cap: CAP } }
}

phase('Plan')
let areas = flags.areas.list
if (!areas) {
  const A = { type: 'object', required: ['areas'], properties: { areas: { type: 'array', items: {
    type: 'object', required: ['area', 'source'], properties: {
      area: { type: 'string' }, source: { type: 'string', enum: ['local', 'dep', 'web'] } } } } } }
  const planned = (await named('plan', 'Plan ' + flags.areas.count + ' distinct investigation areas for: ' + prompt + '. Tag each source local|dep|web.',
    { phase: 'Plan', agentType: RESEARCHER, schema: A })).areas
  // The budget guard bounded the REQUESTED count; hold the plan agent to it,
  // or an over-generous plan re-admits the cap-death past the guard.
  if (planned.length > flags.areas.count) log('plan returned ' + planned.length + ' areas, clamping to the requested ' + flags.areas.count)
  areas = planned.slice(0, flags.areas.count).map(a => a.area + ' [' + a.source + ']')
}
log('prior-art: areas=' + areas.length + ' verify=' + flags['verify-scope'])

const CLAIM = { type: 'object', required: ['claims'], properties: { claims: { type: 'array', items: {
  type: 'object', required: ['text'], properties: {
    text: { type: 'string' }, source: { type: 'string' }, loadBearing: { type: 'boolean' } } } } } }
const VERDICT = { type: 'object', required: ['refuted'], properties: { refuted: { type: 'boolean' }, correction: { type: 'string' } } }

// codex search leg: a different vendor's model AND retrieval stack digging the
// same prior-art question -- recall the same-model deep-dives miss. Live web
// search proven on codex 0.144.1. Claims PREPEND to the dig pool and ride the
// same dedup + verify scoping; the verify cap gets +4 headroom so the extra
// leg cannot crowd deep-dive claims out. Dead leg (auth/exec error) stays
// distinguishable from "found nothing" via available=false.
const CODEX_CLAIMS = { type: 'object', required: ['available', 'claims', 'dropped'], properties: {
  available: { type: 'boolean' }, error: { type: 'string' }, dropped: { type: 'integer' },
  claims: { type: 'array', items: { type: 'object', required: ['text'], properties: {
    text: { type: 'string' }, source: { type: 'string' }, loadBearing: { type: 'boolean' } } } } } }
let codexDown = false, codexDropped = 0
const codexLeg = flags.codex !== 'on' ? null : () => named('dig:codex',
  'Cross-model search leg: relay codex, do NOT add claims of your own.\n' +
  '1. Write the research question below to a temp file (mktemp; a quoted bash heredoc is quoting-safe -- the question may contain $, backticks, or quotes), then run:\n' +
  '   codex exec -s read-only -c \'tools.web_search=true\' -o <mktemp-out> "Read <input-file>. Research prior art for that question with live web search. Return concrete claims about how existing systems solve it, each on its own line with a source URL; end load-bearing claims with [load-bearing]."\n' +
  '2. Read the output file. Drop claims with no source URL and count them in dropped; keep codex\'s wording in text and map [load-bearing] to loadBearing=true.\n' +
  '3. Return available=true with the surviving claims. If codex exits nonzero or errors (a 401 after retry churn means auth expired), return available=false with the first error lines in error. Never fabricate claims.\n' +
  'Research question:\n' + prompt,
  { phase: 'Dig', agentType: RESEARCHER, schema: CODEX_CLAIMS })

phase('Dig')
// plate grammar bans ] " \ in legs (see the named() comment); claim/area text is untrusted.
const plateLeg = (s) => String(s || '').replace(/[\]"\\]/g, '').slice(0, 24)
const thunks = areas.map(a => () => named('dig:' + plateLeg(a.split(' [')[0]),
  'Deep-dive prior art for "' + prompt + '", area: ' + a + '. Prefer primary sources; cite each claim with a source. Mark load-bearing claims.',
  { phase: 'Dig', agentType: RESEARCHER, schema: CLAIM }))
if (codexLeg) thunks.unshift(codexLeg)
const results = (await parallel(thunks)).filter(Boolean)
let found = []
for (const r of results) {
  if (r.available === undefined) { found = found.concat(r.claims || []); continue }
  // the codex leg's shape carries availability alongside claims
  if (!r.available) { codexDown = true; log('codex search leg DOWN: ' + (r.error || 'no error text')) }
  else { codexDropped = r.dropped || 0; found = (r.claims || []).concat(found) }
}
if (codexLeg && !results.some(r => r.available !== undefined)) {
  codexDown = true
  log('codex search leg DOWN: agent died')
}
const seen = new Set()
const fresh = found.filter(c => {
  const k = (c.text || '').slice(0, 80).toLowerCase()
  if (seen.has(k)) return false; seen.add(k); return true
})
log('prior-art: ' + found.length + ' claims, ' + fresh.length + ' fresh')

phase('Verify')
// verified holds ONLY claims that survived the skeptic quorum. Everything not
// refuted but never actually checked -- out of verify scope, over cap, crashed
// verifiers, or verify-scope=none -- lands in the unverified pool and reaches
// synthesis labeled as such, never laundered into "verified".
const corrections = []
const verified = [], unverifiedPool = []
let refutedCount = 0, failedVerifiers = 0, overCap = 0, checkedCount = 0
if (flags['verify-scope'] === 'none') {
  unverifiedPool.push(...fresh)
} else {
  const targets = flags['verify-scope'] === 'all' ? fresh : fresh.filter(c => c.loadBearing)
  unverifiedPool.push(...fresh.filter(c => !targets.includes(c)))   // out of scope: never checked
  // budget-derived ceiling: a low budget cuts the verify cap to 0 (loud, -> UNVERIFIED).
  const vcap = budgetLow(budget, BUDGET_RESERVE) ? 0 : CAP
  if (vcap < CAP) log('budget guard: ' + budget.remaining() + ' of ' + budget.total + ' left -- verify cap cut to 0')
  const { take, over } = capClaims(targets, vcap)
  if (over.length) log('verify cap: verified ' + take.length + '/' + targets.length + ', ' + over.length + ' over cap -> UNVERIFIED')
  overCap = over.length
  checkedCount = take.length
  unverifiedPool.push(...over)
  const judged = await parallel(take.map(c => () =>
    parallel(Array.from({ length: flags.votes }, (_, k) => () =>
      named('verify:' + plateLeg(c.text), 'Skeptic ' + (k + 1) + ': try to REFUTE this prior-art claim. Default refuted=true if you cannot confirm from a primary source. If it is partly wrong, give a correction.\nClaim: ' + c.text + '\nSource: ' + (c.source || 'none'),
        { phase: 'Verify', agentType: SKEPTIC, schema: VERDICT })))
      .then(votes => {
        const vv = votes.filter(Boolean)   // filter raw votes BEFORE wrapping: a wrapped null verdict is a truthy object
        for (const v of vv) if (v.correction) corrections.push({ claim: c.text, correction: v.correction })
        return { claim: c, verdict: tallyVotes(vv, flags.votes), failed: flags.votes - vv.length }
      })))
  for (const j of judged.filter(Boolean)) {
    if (j.verdict === 'CONFIRMED') verified.push(j.claim)
    else if (j.verdict === 'UNVERIFIED') unverifiedPool.push(j.claim)
    else refutedCount++
    failedVerifiers += j.failed
  }
  log('prior-art: confirmed ' + verified.length + ', refuted ' + refutedCount + ', unverified ' + unverifiedPool.length + ', ' + corrections.length + ' corrections')
}

phase('Synthesize')
// The report keeps its breadth, but the two pools reach synthesis separately
// with an honest basis note -- mirror of aw-research's basisNote pattern.
const basisNote = flags['verify-scope'] === 'none'
  ? 'verification was DISABLED (verify-scope=none): every claim below is UNVERIFIED; label the whole report unverified'
  : verified.length
    ? 'VERIFIED claims each survived a ' + flags.votes + '-skeptic refutation quorum; UNVERIFIED claims were never checked (out of verify scope, over cap, or verifier crash) -- usable for breadth, but label any load-bearing use of them as unverified'
    : checkedCount === 0
      ? 'NOTHING was actually checked (no claim in verify scope, or the budget guard cut the cap to 0): every claim below is UNVERIFIED; label the whole report unverified'
      : 'NO claim survived verification: every claim below is UNVERIFIED; label the whole report unverified'
const report = await named('synthesize',
  'Write a cited report answering: ' + prompt + '\nBasis: ' + basisNote + '\n' +
  'VERIFIED claims:\n' +
  (verified.map((c, i) => '[' + (i + 1) + '] ' + c.text + ' (' + (c.source || 'unsourced') + ')').join('\n') || '(none)') +
  '\nUNVERIFIED claims:\n' +
  (unverifiedPool.map((c, i) => '[U' + (i + 1) + '] ' + c.text + ' (' + (c.source || 'unsourced') + ')').join('\n') || '(none)') +
  '\nEnd with a "Corrections from verification" section listing:\n' + JSON.stringify(corrections, null, 2),
  { phase: 'Synthesize' })

return { question: prompt, areas, verified: verified.length, refuted: refutedCount, unverified: unverifiedPool.length, failedVerifiers, corrections: corrections.length, overCap, codexDown, codexDropped, report }
