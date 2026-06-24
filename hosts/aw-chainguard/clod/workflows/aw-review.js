export const meta = {
  name: 'aw-review',
  description: '[scope=diff lang=auto lenses=correctness,error-handling,concurrency,security votes=3 passes=2 severity-floor=med intensity=5 subagents=custom|stock] Adversarial review: fan out over lenses, refute-verify every finding, loop until a pass finds nothing new, synthesize one severity-ranked verdict. Informal word=value flags (long or short, anywhere in the prompt).',
  whenToUse: 'Reviewing a diff or path; tune scope, lang, lenses, votes, passes, severity-floor',
  phases: [{ title: 'Scope' }, { title: 'Review' }, { title: 'Verify' }, { title: 'Synthesize' }],
  flags: {
    scope:  { short: 'c', type: 'str',  default: 'diff', help: 'PR ref, git range, or path' },
    lang:   { short: 'g', type: 'str',  default: 'auto', help: 'language for the idioms lens; auto-detected from the change' },
    lenses: { short: 'l', type: 'axes', default: { list: ['correctness', 'error-handling', 'concurrency', 'security'] }, help: 'review dimensions; a single N auto-derives N lenses' },
    votes:  { short: 'v', type: 'int',  default: 3, min: 1, max: 5, help: 'skeptics per finding' },
    passes: { short: 'p', type: 'int',  default: 2, min: 1, max: 6, help: 'loop-until-dry re-review rounds' },
    'severity-floor': { short: 'y', type: 'str', default: 'med', choices: ['low', 'med', 'high'], help: 'lowest severity that gets verified' },
    intensity: { short: 'i', type: 'int', default: 5, min: 0, max: 10, help: 'one knob scaling unset votes/passes/lens-count' },
    subagents: { short: 's', type: 'str', default: 'custom', choices: ['custom', 'stock'], help: 'stock drops the custom agent types' },
  },
}

// Examples:
//   review scope=#41344 votes=3
//   review scope=HEAD~5..HEAD lenses=correctness,security severity-floor=high

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
  for (const [flag, val] of [['votes', k.votes], ['passes', k.passes]])
    if (!set.has(flag)) flags[flag] = val
  if (!set.has('lenses') && flags.lenses && flags.lenses.count != null) flags.lenses = { count: k.fanout }
}
const stock = flags.subagents === 'stock'

const REVIEWER = stock ? undefined : 'reviewer'
const SKEPTIC = stock ? undefined : 'skeptic'

// Phase 0: derive the concrete change-set from scope (never hardcode a file list).
phase('Scope')
const SCOPE = { type: 'object', required: ['target', 'files'], properties: {
  target: { type: 'string' }, files: { type: 'array', items: { type: 'string' } },
  lang: { type: 'string' } } }
const scoped = await agent(
  'Resolve the review scope "' + flags.scope + '" into a concrete change-set. ' +
  'If it is a PR number or git range, run git to get the changed files; if a path, list the relevant source files (git ls-files). ' +
  'Return the human label (target), the file list, and lang = the dominant source language of the change (e.g. go, rust, nix, cue, python, ts).',
  { label: 'scope', phase: 'Scope', agentType: REVIEWER, schema: SCOPE })
const focus = prompt || 'the change as a whole'
const lang = flags.lang !== 'auto' ? flags.lang : (scoped.lang || '')

// Resolve lenses: explicit list, or derive N from the change-set. When the default
// list stands (lenses not set by hand), append a language-specific idioms lens.
let lenses = flags.lenses.list
if (!lenses) {
  const L = { type: 'object', required: ['lenses'], properties: { lenses: { type: 'array', items: { type: 'string' } } } }
  const d = await agent(
    'Propose ' + flags.lenses.count + ' orthogonal review dimensions for this change (target: ' + scoped.target + '). Distinct angles, no overlap.',
    { label: 'derive-lenses', phase: 'Scope', agentType: REVIEWER, schema: L })
  lenses = d.lenses
} else if (!set.has('lenses') && lang) {
  lenses = lenses.concat(lang + ' idioms & footguns')
}
log('review: scope=' + scoped.target + ' lang=' + (lang || 'n/a') + ' lenses=' + lenses.join('+') + ' votes=' + flags.votes + ' passes=' + flags.passes + ' floor=' + flags['severity-floor'])

const FIND = { type: 'object', required: ['findings'], properties: { findings: { type: 'array', items: {
  type: 'object', required: ['file', 'desc', 'severity'], properties: {
    file: { type: 'string' }, line: { type: 'integer' }, lens: { type: 'string' },
    severity: { type: 'string', enum: ['critical', 'high', 'med', 'low'] }, desc: { type: 'string' } } } } } }
const VERDICT = { type: 'object', required: ['real'], properties: { real: { type: 'boolean' }, why: { type: 'string' } } }

// severity floor: below-floor findings are carried but tagged UNVERIFIED, never verified, never in the confirmed tally.
const order = { low: 0, med: 1, high: 2, critical: 3 }
const floor = order[flags['severity-floor']] != null ? order[flags['severity-floor']] : 1
const seen = new Set()                       // dedup across ALL rounds: never verify the same defect twice
const confirmed = [], unverified = [], belowFloor = [], allJudged = []
let foundTotal = 0, freshTotal = 0

// Loop-until-dry: each round, reviewers hunt for issues NOT already surfaced.
// Stop when a round finds nothing fresh, or passes is exhausted.
for (let round = 0; round < flags.passes; round++) {
  phase('Review')
  const known = confirmed.concat(unverified, belowFloor).map(f => f.file + ':' + f.line + ' ' + f.desc).slice(0, 60)
  const knownNote = known.length
    ? '\nAlready found (do NOT re-report these; look for issues NOT in this list):\n' + known.join('\n')
    : ''
  const found = (await parallel(lenses.map(lens => () => agent(
    'Round ' + (round + 1) + '. Review ' + scoped.target + ' through the ' + lens + ' lens. Focus: ' + focus + '. Files:\n' + scoped.files.join('\n') +
    knownNote + '\n' +
    'Find real, specific issues (file:line). Be concrete; no style nits, no speculation.',
    { label: 'review:' + lens + ':r' + (round + 1), phase: 'Review', agentType: REVIEWER, schema: FIND }))))
    .filter(Boolean).flatMap(r => r.findings || [])
  foundTotal += found.length

  const fresh = found.filter(f => {
    const k = (f.file + ':' + f.line + ':' + (f.desc || '')).toLowerCase()
    if (seen.has(k)) return false; seen.add(k); return true
  })
  freshTotal += fresh.length
  if (!fresh.length) { log('round ' + (round + 1) + ': no fresh findings -- dry, stopping'); break }

  const toVerify = fresh.filter(f => (order[f.severity] || 0) >= floor)
  belowFloor.push(...fresh.filter(f => (order[f.severity] || 0) < floor).map(f => ({ ...f, verdict: 'UNVERIFIED' })))
  log('round ' + (round + 1) + ': ' + found.length + ' found, ' + fresh.length + ' fresh, ' + toVerify.length + ' at/above floor')

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
  allJudged.push(...judged)
  confirmed.push(...judged.filter(f => f.verdict === 'CONFIRMED'))
  unverified.push(...judged.filter(f => f.verdict === 'UNVERIFIED'))
}

phase('Synthesize')
const report = await agent(
  'Apply the synthesis discipline in ~/.claude/workflows/partials/SYNTHESIS.md (read it first).\n' +
  'Summarize this review of ' + scoped.target + '. CONFIRMED findings (survived a ' + flags.votes + '-vote refutation):\n' +
  JSON.stringify(confirmed, null, 2) + '\n' +
  belowFloor.length + ' below-floor findings exist and are UNVERIFIED - do not present them as confirmed. ' +
  'Group confirmed by severity with a one-line fix each.',
  { label: 'synthesize', phase: 'Synthesize' })

const tally = {
  found: foundTotal, fresh: freshTotal, confirmed: confirmed.length,
  refuted: allJudged.filter(f => f.verdict === 'REFUTED').length,
  unverified: unverified.length + belowFloor.length,
  failedVerifiers: allJudged.reduce((a, f) => a + (f.failed || 0), 0),
}
return { scope: scoped.target, lenses, tally, confirmed, report }
