export const meta = {
  name: 'aw-design-review',
  description: '[design= code-root=. lenses=feasibility,semantics,scale,hermeticity,ordering locked= votes=3 codex=on intensity=5 subagents=custom|stock] Stress-test a design through heterogeneous lenses (plus a cross-model codex dissent leg) with a refute-default verifier; design= is a path to the doc under review, unset means the prompt is the design problem and a candidate is proposed; reconcile confirmed flaws into a prioritized change list. word=value flags (long or short, anywhere in the prompt).',
  whenToUse: 'Reviewing a design doc or a design problem; tune design, code-root, lenses, locked',
  phases: [{ title: 'Frame' }, { title: 'Critique' }, { title: 'Verify' }, { title: 'Synthesize' }],
}

// Flag specs: single source of truth for parseFlags AND the lint/cheatsheet
// extractors, which slice this literal textually (closing brace at col 0).
// Lives OUTSIDE meta: the Workflow runtime strips the meta export before
// running the body, so the body can only reach a plain const.
const FLAGS = {
  design:      { short: 'd', type: 'path', default: '', help: 'path to the design doc under review (unset: the prompt is the problem)' },
  'code-root': { short: 'r', type: 'str',  default: '.', help: 'code root to ground flaws in' },
  lenses:      { short: 'l', type: 'axes', default: { list: ['feasibility', 'semantics', 'scale', 'hermeticity', 'ordering'] }, help: 'stress-test lenses; a single N auto-derives N' },
  locked:      { short: 'k', type: 'path', default: '', help: 'path to a CONSTRAINTS / LOCKED block' },
  votes:       { short: 'v', type: 'int',  default: 3, min: 1, max: 5, help: 'skeptics per flaw' },
  codex:       { short: 'x', type: 'str',  default: 'on', choices: ['on', 'off'], help: 'cross-model codex dissent leg in the critique fan-out' },
  intensity:   { short: 'i', type: 'int',  default: 5, min: 0, max: 10, help: 'one knob scaling unset votes/lens-count' },
  subagents:   { short: 's', type: 'str',  default: 'custom', choices: ['custom', 'stock'], help: 'stock drops the custom agent types' },
}

// Examples:
//   design-review design=docs/DESIGN.md code-root=internal/can
//   design-review code-root=internal/gaffe lenses=feasibility,scale should gaffe materialize lazily?

// flags: <word>=<value> or <short>=<value>, pulled from ANYWHERE in the prompt;
// remaining tokens (in order) are the focus prompt. unknown word=value stays verbatim.
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
// cap: the ceiling on flaws entering the verify fan-out.
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
  // intensity scales votes (and the lenses count) -- this workflow's only knobs.
  if (!set.has('votes')) flags.votes = k.votes
  if (!set.has('lenses') && flags.lenses && flags.lenses.count != null) flags.lenses = { count: k.fanout }
  return flags
}
applyIntensity(flags, set)
const DEFAULT_INTENSITY = 5
const VERIFY_CAP = fromIntensity(set.has('intensity') ? flags.intensity : DEFAULT_INTENSITY).cap
// loud cap split: callers log take/over and tag over UNVERIFIED -- never a silent slice.
function capClaims(list, cap) {
  return { take: list.slice(0, cap), over: list.slice(cap) }
}
// sub-quorum (verifiers crashed) -> UNVERIFIED, never laundered into a verdict;
// majority is over the requested total, so missing votes count against confirmation.
function tallyVotes(vv, total) {
  if (vv.length < Math.ceil(total / 2)) return 'UNVERIFIED'
  return vv.filter(x => x.real).length > total / 2 ? 'CONFIRMED' : 'REFUTED'
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
// design= is path-or-unset: set -> the file IS the design (kind=doc); unset -> the
// prompt is the design problem (kind=proposed) and must be a real problem statement.
function designKind(design, prompt) {
  if (design) return { kind: 'doc' }
  if ((prompt || '').trim().length >= 20) return { kind: 'proposed' }
  return { error: 'kind=proposed requires a non-trivial problem statement (>= 20 chars)' }
}
// lens count is pre-spawn knowable either way (axes count is unclamped, so a
// lenses=999 invocation is legal without this). overhead: locked + frame +
// derive-lenses + codex leg + synthesize.
const lensCount = flags.lenses.list ? flags.lenses.list.length : flags.lenses.count
const AGENT_BUDGET = 900
const worst = worstCaseAgents(1, lensCount, flags.votes, VERIFY_CAP, (flags.codex === 'on' ? 1 : 0) + 4)
if (worst > AGENT_BUDGET) {
  log('rejected: worst-case ' + worst + ' agents > ' + AGENT_BUDGET + ' (runtime lifetime cap is 1000) -- lower lenses/votes/intensity')
  return { error: 'agent budget: worst case ' + worst + ' exceeds ' + AGENT_BUDGET,
           rejected: { lenses: lensCount, votes: flags.votes, cap: VERIFY_CAP } }
}
const stock = flags.subagents === 'stock'

const DESIGNER = stock ? undefined : 'designer'
const SKEPTIC = stock ? undefined : 'skeptic'

// name-plate: prefix every agent prompt with [<wf>:<leg>]. The plate is the
// ONLY channel that reaches external observers -- khudson's clod panel reads
// transcript heads for leg names and the run name; label: feeds only the
// in-app progress UI. Keep legs short ASCII (<=60 chars, no ] or "), so
// pathy legs use basenames. Copied verbatim across every aw-*.js (only WF
// differs).
const WF = 'aw-design-review'
const named = (leg, prompt, opts) => agent('[' + WF + ':' + leg + '] ' + prompt, { label: leg, ...opts })

const kind = designKind(flags.design, prompt)
if (kind.error) { log('rejected: "' + prompt + '" -- ' + kind.error); return { error: kind.error, rejected: prompt } }

// Phase 0: load the LOCKED constraints (found-checked); frame the design per designKind.
phase('Frame')
let locked = ''
if (flags.locked) {
  const LK = { type: 'object', required: ['found', 'text'], properties: { found: { type: 'boolean' }, text: { type: 'string' } } }
  const lk = await named('locked', 'Read the constraints file ' + flags.locked + ' and return its text verbatim as the LOCKED block. If the file does not exist or cannot be read, return found=false and empty text.',
    { phase: 'Frame', agentType: DESIGNER, schema: LK })
  if (!lk.found || !(lk.text || '').trim()) {
    log('locked constraints unreadable: ' + flags.locked)
    return { error: 'locked= file missing or unreadable', rejected: flags.locked }
  }
  locked = lk.text
}
const FRAME_DOC = { type: 'object', required: ['found', 'kind', 'design'], properties: {
  found: { type: 'boolean' }, kind: { type: 'string', enum: ['doc', 'proposed'] }, design: { type: 'string' } } }
const FRAME_PROPOSED = { type: 'object', required: ['kind', 'design'], properties: {
  kind: { type: 'string', enum: ['doc', 'proposed'] }, design: { type: 'string' } } }
const framed = await named('frame',
  (kind.kind === 'doc'
    ? 'Read the design at ' + flags.design + ' and summarize the approach to be reviewed (kind=doc). If the file does not exist or cannot be read, return found=false and empty design.'
    : 'This is a design PROBLEM, not a doc: "' + prompt + '". Propose ONE concrete candidate approach to review (kind=proposed), grounded in ' + flags['code-root'] + '.') +
  (locked ? '\nLOCKED constraints (do not violate or relitigate):\n' + locked : ''),
  { phase: 'Frame', agentType: DESIGNER, schema: kind.kind === 'doc' ? FRAME_DOC : FRAME_PROPOSED })
if (kind.kind === 'doc' && (!framed.found || !(framed.design || '').trim())) {
  log('design doc unreadable: ' + flags.design)
  return { error: 'design= file missing or unreadable', rejected: flags.design }
}

let lenses = flags.lenses.list
if (!lenses) {
  const L = { type: 'object', required: ['lenses'], properties: { lenses: { type: 'array', items: { type: 'string' } } } }
  lenses = (await named('derive-lenses', 'Propose ' + flags.lenses.count + ' orthogonal stress-test lenses for this design:\n' + framed.design,
    { phase: 'Frame', agentType: DESIGNER, schema: L })).lenses
}
log('design-review: ' + framed.kind + ' lenses=' + lenses.join('+') + (locked ? ' (locked)' : ''))

const FLAW = { type: 'object', required: ['flaws'], properties: { flaws: { type: 'array', items: {
  type: 'object', required: ['lens', 'desc', 'severity'], properties: {
    lens: { type: 'string' }, desc: { type: 'string' }, evidence: { type: 'string' },
    severity: { type: 'string', enum: ['fatal', 'major', 'minor'] } } } } } }
const VERDICT = { type: 'object', required: ['real'], properties: { real: { type: 'boolean' }, why: { type: 'string' } } }

phase('Critique')
// codex dissent leg: cross-model recall (same-model lens critics share one
// model's blind spots; a flaw Claude cannot see never reaches Verify). Codex
// flaws enter as CANDIDATES through the same dedup + skeptic quorum as every
// lens flaw -- codex supplies recall, the quorum supplies precision. The leg
// is a Claude relay (DESIGNER has Bash; codex exec is allowlisted) that must
// ground every codex citation before relaying. A dead leg (auth, exec error,
// or a null agent) is DOWN: logged and tallied, never absorbed by
// filter(Boolean) -- available=false is what keeps "codex is down"
// distinguishable from "codex found nothing".
const CODEX_FLAWS = { type: 'object', required: ['available', 'flaws', 'dropped'], properties: {
  available: { type: 'boolean' }, error: { type: 'string' }, dropped: { type: 'integer' },
  flaws: { type: 'array', items: {
    type: 'object', required: ['lens', 'desc', 'severity'], properties: {
      lens: { type: 'string' }, desc: { type: 'string' }, evidence: { type: 'string' },
      severity: { type: 'string', enum: ['fatal', 'major', 'minor'] } } } } } }
const legThunks = lenses.map(lens => () => named('critique:' + lens,
  'Stress-test this design through the ' + lens + ' lens. Ground every flaw in real code under ' + flags['code-root'] + ' (file:line).\n' +
  'Design:\n' + framed.design + (locked ? '\nLOCKED (do not flag these as flaws):\n' + locked : ''),
  { phase: 'Critique', agentType: DESIGNER, schema: FLAW }))
if (flags.codex === 'on') legThunks.unshift(() => named('critique:codex-dissent',
  'Cross-model dissent leg: relay codex, do NOT add flaws of your own.\n' +
  '1. Check auth first: run "codex login status" if permitted; if it prompts or fails, skip straight to the codex exec attempt.\n' +
  '2. Write the design brief below to a temp file (mktemp; bash heredoc is quoting-safe), then run:\n' +
  '   codex exec -s read-only -o <mktemp-out> "Read <input-file>. Attack the design described there: list every concrete flaw, each grounded in real code under ' + flags['code-root'] + ' with file:line, severity fatal|major|minor. Be adversarial; no praise."\n' +
  '3. Read the output file. For EACH codex flaw, verify the cited file/symbol actually exists (Read/Grep) before relaying; drop ungroundable ones and count them in dropped.\n' +
  '4. Return available=true with the surviving flaws (keep codex\'s wording in desc, cite the grounded file:line in evidence). If codex exits nonzero, times out, or errors (e.g. a 401 after retry churn -- that means auth expired), return available=false with the first error lines in error. Never fabricate flaws.\n' +
  'Design brief:\n' + framed.design + (locked ? '\nLOCKED (not flaws, do not relay complaints about these):\n' + locked : ''),
  { phase: 'Critique', agentType: DESIGNER, schema: CODEX_FLAWS }))
const legResults = await parallel(legThunks)
let codexDown = false, codexDropped = 0, codexFlaws = []
if (flags.codex === 'on') {
  const cx = legResults[0]
  if (!cx || cx.available === false) {
    codexDown = true
    log('codex dissent leg DOWN: ' + (cx ? (cx.error || 'no error text') : 'agent died'))
  } else {
    codexDropped = cx.dropped || 0
    codexFlaws = (cx.flaws || []).map(f => ({ ...f, lens: 'codex-dissent' }))
    if (codexDropped) log('codex dissent: ' + codexFlaws.length + ' flaw(s) relayed, ' + codexDropped + ' ungroundable dropped')
  }
}
// codex flaws concat at the HEAD so the run's only cross-model findings cannot
// silently fall over the verify cap.
const lensResults = flags.codex === 'on' ? legResults.slice(1) : legResults
const found = codexFlaws.concat(lensResults.filter(Boolean).flatMap(r => r.flaws || []))
const seen = new Set()
const fresh = found.filter(f => {
  const k = (f.lens + ':' + (f.desc || '')).toLowerCase()
  if (seen.has(k)) return false; seen.add(k); return true
})
log('design-review: ' + found.length + ' flaws, ' + fresh.length + ' fresh' + (codexDown ? ' (codex leg DOWN)' : ''))

phase('Verify')
// budget-derived ceiling: a low budget cuts the verify cap to 0 (loud, -> UNVERIFIED).
const vcap = budgetLow(budget, BUDGET_RESERVE) ? 0 : VERIFY_CAP
if (vcap < VERIFY_CAP) log('budget guard: ' + budget.remaining() + ' of ' + budget.total + ' left -- verify cap cut to 0')
const { take, over } = capClaims(fresh, vcap)
if (over.length) log('verify cap: verified ' + take.length + '/' + fresh.length + ', ' + over.length + ' over cap -> UNVERIFIED')
const judged = (await parallel(take.map(f => () =>
  parallel(Array.from({ length: flags.votes }, (_, v) => () =>
    named('verify:' + f.lens, 'Skeptic ' + (v + 1) + ': is this a REAL flaw in the design, or a misread? Default real=false unless you confirm against the code/design. Check it is not a LOCKED choice.\n' +
      f.lens + ': ' + f.desc + '\nEvidence: ' + (f.evidence || 'none'),
      { phase: 'Verify', agentType: SKEPTIC, schema: VERDICT })))
    .then(votes => {
      const vv = votes.filter(Boolean)
      return { ...f, verdict: tallyVotes(vv, flags.votes) }
    })))).filter(Boolean)
const confirmed = judged.filter(f => f.verdict === 'CONFIRMED')
const unverified = judged.filter(f => f.verdict === 'UNVERIFIED').concat(over.map(f => ({ ...f, verdict: 'UNVERIFIED' })))

phase('Synthesize')
const fatal = confirmed.some(f => f.severity === 'fatal')
const report = await named('synthesize',
  'Apply the synthesis discipline in ~/.claude/workflows/partials/SYNTHESIS.md and the design doctrine in ~/.claude/workflows/partials/DESIGN_DOCTRINE.md (read both first).\n' +
  'Synthesize a design review. CONFIRMED flaws (verified against code):\n' + JSON.stringify(confirmed, null, 2) + '\n' +
  'A single fatal flaw vetoes the design regardless of the rest (there ' + (fatal ? 'IS' : 'is NO') + ' confirmed fatal flaw). ' +
  'Output a prioritized change list (fatal, then major, then minor), each grounded in file:line, ending in one go/no-go.',
  { phase: 'Synthesize' })

return { kind: framed.kind, lenses, confirmed: confirmed.length, unverified: unverified.length, fatal, overCap: over.length, codexDown, codexDropped, report }
