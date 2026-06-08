export const meta = {
  name: 'aw-review',
  description: '[scope=diff lenses=correctness,perf,security votes=3 severity-floor=med intensity=5 subagents=custom|stock] Adversarial review: fan out over lenses, refute-verify every finding, synthesize one severity-ranked verdict. Informal word=value flags.',
  whenToUse: 'Reviewing a diff or path; tune scope, lenses, votes, severity-floor',
  phases: [{ title: 'Scope' }, { title: 'Review' }, { title: 'Verify' }, { title: 'Synthesize' }],
}

// Examples:
//   review scope=#41344 votes=3
//   review scope=HEAD~5..HEAD lenses=correctness,security severity-floor=high

// ── informal flags: <word>=<value> up front, then the optional focus prompt. ──
function coerce(v, s) {
  if (s.type === 'int') {
    let n = parseInt(v, 10); if (isNaN(n)) n = s.default
    if (s.min != null) n = Math.max(s.min, n)
    if (s.max != null) n = Math.min(s.max, n)
    return n
  }
  if (s.type === 'list') { const _p = String(v).split(',').map(x => x.trim()).filter(Boolean); return _p.length ? _p : s.default }
  if (s.type === 'axes') {
    const p = String(v).split(',').map(x => x.trim()).filter(Boolean)
    if (p.length === 0) return s.default
    return (p.length === 1 && /^\d+$/.test(p[0])) ? { count: Math.max(1, parseInt(p[0], 10)) } : { list: p }
  }
  return String(v)
}
function parseFlags(raw, spec) {
  const flags = {}; for (const k in spec) flags[k] = spec[k].default
  const set = new Set()
  const text = (typeof raw === 'string' ? raw : (raw && raw.prompt) || '').trim()
  const toks = text.length ? text.split(/\s+/) : []
  let i = 0
  for (; i < toks.length; i++) {
    const m = /^([A-Za-z][A-Za-z0-9_-]*)=(.*)$/.exec(toks[i])
    if (!m || !(m[1] in spec)) break
    flags[m[1]] = coerce(m[2], spec[m[1]])
    set.add(m[1])
  }
  return { flags, prompt: toks.slice(i).join(' '), set }
}

const { flags, prompt, set } = parseFlags(args, {
  scope: { type: 'str', default: 'diff' },                       // PR ref, git range, or path
  lenses: { type: 'axes', default: { list: ['correctness', 'perf', 'security'] } },
  votes: { type: 'int', default: 3, min: 1, max: 5 },
  'severity-floor': { type: 'str', default: 'med' },             // high|med|low: floor that gets verified
  intensity: { type: 'int', default: 5, min: 0, max: 10 },
  subagents: { type: 'str', default: 'custom' },
})

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
  for (const [flag, val] of [['votes', k.votes], ['verify', k.votes], ['fanout', k.fanout], ['passes', k.passes]])
    if (flag in flags && !set.has(flag)) flags[flag] = val
  if (!set.has('lenses') && flags.lenses && flags.lenses.count != null) flags.lenses = { count: k.fanout }
}
const stock = flags.subagents === 'stock'

const REVIEWER = stock ? undefined : 'reviewer'
const SKEPTIC = stock ? undefined : 'skeptic'

// Phase 0: derive the concrete change-set from scope (never hardcode a file list).
phase('Scope')
const SCOPE = { type: 'object', required: ['target', 'files'], properties: {
  target: { type: 'string' }, files: { type: 'array', items: { type: 'string' } } } }
const scoped = await agent(
  'Resolve the review scope "' + flags.scope + '" into a concrete change-set. ' +
  'If it is a PR number or git range, run git to get the changed files; if a path, list the relevant source files (git ls-files). ' +
  'Return the human label (target) and the file list.',
  { label: 'scope', phase: 'Scope', agentType: REVIEWER, schema: SCOPE })
const focus = prompt || 'the change as a whole'

// Resolve lenses: explicit list, or derive N from the change-set.
let lenses = flags.lenses.list
if (!lenses) {
  const L = { type: 'object', required: ['lenses'], properties: { lenses: { type: 'array', items: { type: 'string' } } } }
  const d = await agent(
    'Propose ' + flags.lenses.count + ' orthogonal review dimensions for this change (target: ' + scoped.target + '). Distinct angles, no overlap.',
    { label: 'derive-lenses', phase: 'Scope', agentType: REVIEWER, schema: L })
  lenses = d.lenses
}
log('review: scope=' + scoped.target + ' lenses=' + lenses.join('+') + ' votes=' + flags.votes + ' floor=' + flags['severity-floor'])

const FIND = { type: 'object', required: ['findings'], properties: { findings: { type: 'array', items: {
  type: 'object', required: ['file', 'desc', 'severity'], properties: {
    file: { type: 'string' }, line: { type: 'integer' }, lens: { type: 'string' },
    severity: { type: 'string', enum: ['critical', 'high', 'med', 'low'] }, desc: { type: 'string' } } } } } }
const VERDICT = { type: 'object', required: ['real'], properties: { real: { type: 'boolean' }, why: { type: 'string' } } }

phase('Review')
const found = (await parallel(lenses.map(lens => () => agent(
  'Review ' + scoped.target + ' through the ' + lens + ' lens. Focus: ' + focus + '. Files:\n' + scoped.files.join('\n') + '\n' +
  'Find real, specific issues (file:line). Be concrete; no style nits, no speculation.',
  { label: 'review:' + lens, phase: 'Review', agentType: REVIEWER, schema: FIND }))))
  .filter(Boolean).flatMap(r => r.findings || [])

// dedup before verify (do not verify the same defect twice).
const seen = new Set()
const fresh = found.filter(f => {
  const k = (f.file + ':' + f.line + ':' + (f.desc || '')).toLowerCase()
  if (seen.has(k)) return false; seen.add(k); return true
})

// severity floor: below-floor findings are carried but tagged UNVERIFIED, never verified, never in the confirmed tally.
const order = { low: 0, med: 1, high: 2, critical: 3 }
const floor = order[flags['severity-floor']] != null ? order[flags['severity-floor']] : 1
const toVerify = fresh.filter(f => (order[f.severity] || 0) >= floor)
const belowFloor = fresh.filter(f => (order[f.severity] || 0) < floor).map(f => ({ ...f, verdict: 'UNVERIFIED' }))
log('review: ' + found.length + ' found, ' + fresh.length + ' fresh, ' + toVerify.length + ' at/above floor, ' + belowFloor.length + ' below floor (carried, unverified)')

phase('Verify')
const judged = (await parallel(toVerify.map(f => () =>
  parallel(Array.from({ length: flags.votes }, (_, v) => () =>
    agent('Skeptic ' + (v + 1) + ': is this a REAL ' + (f.lens || '') + ' issue, or a false positive? Default real=false unless you confirm by reading the code.\n' + f.file + ':' + f.line + ' - ' + f.desc,
      { label: 'verify:' + f.file, phase: 'Verify', agentType: SKEPTIC, schema: VERDICT })))
    .then(votes => {
      const vv = votes.filter(Boolean)
      const quorum = Math.ceil(flags.votes / 2)
      // sub-quorum (verifiers crashed) -> UNVERIFIED, never laundered into a verdict;
      // majority is over flags.votes, so missing votes count against confirmation.
      const verdict = vv.length < quorum ? 'UNVERIFIED'
        : (vv.filter(x => x.real).length > flags.votes / 2 ? 'CONFIRMED' : 'REFUTED')
      return { ...f, verdict, failed: flags.votes - vv.length }
    })))).filter(Boolean)
const confirmed = judged.filter(f => f.verdict === 'CONFIRMED')
const unverified = judged.filter(f => f.verdict === 'UNVERIFIED')

phase('Synthesize')
const report = await agent(
  'Apply the synthesis discipline in ~/.claude/workflows/partials/SYNTHESIS.md (read it first).\n' +
  'Summarize this review of ' + scoped.target + '. CONFIRMED findings (survived a ' + flags.votes + '-vote refutation):\n' +
  JSON.stringify(confirmed, null, 2) + '\n' +
  belowFloor.length + ' below-floor findings exist and are UNVERIFIED - do not present them as confirmed. ' +
  'Group confirmed by severity with a one-line fix each.',
  { label: 'synthesize', phase: 'Synthesize' })

const tally = {
  found: found.length, fresh: fresh.length, confirmed: confirmed.length,
  refuted: judged.filter(f => f.verdict === 'REFUTED').length,
  unverified: unverified.length + belowFloor.length,
  failedVerifiers: judged.reduce((a, f) => a + (f.failed || 0), 0),
}
return { scope: scoped.target, lenses, tally, confirmed, report }
