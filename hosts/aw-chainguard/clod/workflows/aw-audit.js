export const meta = {
  name: 'aw-audit',
  description: '[repo=. lang=go lenses=bug,test-gap,perf votes=3 intensity=5 subagents=custom|stock] Adversarially audit an existing implementation across discovered packages for bugs/test-gaps/perf; refute-verify each; emit a p0/p1/p2 fix list. word=value flags (long or short, anywhere in the prompt).',
  whenToUse: 'Auditing a rebuilt component; tune repo, lang, lenses, votes',
  phases: [{ title: 'Map' }, { title: 'Audit' }, { title: 'Verify' }, { title: 'Synthesize' }],
}

// Flag specs: single source of truth for parseFlags AND the lint/cheatsheet
// extractors, which slice this literal textually (closing brace at col 0).
// Lives OUTSIDE meta: the Workflow runtime strips the meta export before
// running the body, so the body can only reach a plain const.
const FLAGS = {
  repo:   { short: 'r', type: 'str',  default: '.', help: 'repo or path root to audit' },
  lang:   { short: 'g', type: 'str',  default: 'go', help: 'primary language' },
  lenses: { short: 'l', type: 'axes', default: { list: ['bug', 'test-gap', 'perf'] }, help: 'audit dimensions; a single N auto-derives N lenses' },
  votes:  { short: 'v', type: 'int',  default: 3, min: 1, max: 5, help: 'skeptics per finding' },
  intensity: { short: 'i', type: 'int', default: 5, min: 0, max: 10, help: 'one knob scaling unset votes/lens-count' },
  subagents: { short: 's', type: 'str', default: 'custom', choices: ['custom', 'stock'], help: 'stock drops the custom agent types' },
}

// Examples:
//   audit repo=. lang=go lenses=bug,test-gap,perf votes=5

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
// cap: the ceiling on findings entering the verify fan-out.
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
const stock = flags.subagents === 'stock'

const REVIEWER = stock ? undefined : 'reviewer'
const SKEPTIC = stock ? undefined : 'skeptic'

// Phase 0: discover the package map (never hardcode it).
phase('Map')
const MAP = { type: 'object', required: ['found', 'packages'], properties: {
  found: { type: 'boolean' }, packages: { type: 'array', items: { type: 'string' } } } }
const map = await agent(
  'Map the package/source layout of ' + flags.repo + ' (lang ' + flags.lang + '). Use tree/glob/git ls-files - do not hardcode. Return the packages worth auditing. ' +
  'Return found=false if the repo/path does not exist or cannot be mapped.',
  { label: 'map', phase: 'Map', agentType: REVIEWER, schema: MAP })
if (!map.found || !map.packages.length) {
  log('repo unmappable: ' + flags.repo)
  return { error: 'repo did not resolve to any packages', rejected: flags.repo }
}

let lenses = flags.lenses.list
if (!lenses) {
  const L = { type: 'object', required: ['lenses'], properties: { lenses: { type: 'array', items: { type: 'string' } } } }
  lenses = (await agent(
    'Propose ' + flags.lenses.count + ' orthogonal audit dimensions for a ' + flags.lang + ' component.',
    { label: 'derive-lenses', phase: 'Map', agentType: REVIEWER, schema: L })).lenses
}
const focus = prompt || 'the implementation as a whole'
log('audit: repo=' + flags.repo + ' lang=' + flags.lang + ' pkgs=' + map.packages.length + ' lenses=' + lenses.join('+') + ' votes=' + flags.votes)

const FIND = { type: 'object', required: ['findings'], properties: { findings: { type: 'array', items: {
  type: 'object', required: ['file', 'desc', 'severity'], properties: {
    file: { type: 'string' }, line: { type: 'integer' }, lens: { type: 'string' },
    severity: { type: 'string', enum: ['p0', 'p1', 'p2'] }, desc: { type: 'string' }, test: { type: 'string' } } } } } }
const VERDICT = { type: 'object', required: ['real'], properties: { real: { type: 'boolean' }, why: { type: 'string' } } }

phase('Audit')
const found = (await parallel(lenses.map(lens => () => agent(
  'Audit ' + flags.repo + ' (' + flags.lang + ') through the ' + lens + ' lens. Focus: ' + focus + '. Packages:\n' + map.packages.join('\n') + '\n' +
  'Find real issues (file:line). For each, recommend a concrete ' + flags.lang + ' test mechanism. Severity p0/p1/p2.',
  { label: 'audit:' + lens, phase: 'Audit', agentType: REVIEWER, schema: FIND }))))
  .filter(Boolean).flatMap(r => r.findings || [])

// dedup before verify.
const seen = new Set()
const fresh = found.filter(f => {
  const k = (f.file + ':' + f.line + ':' + (f.desc || '')).toLowerCase()
  if (seen.has(k)) return false; seen.add(k); return true
})
log('audit: ' + found.length + ' found, ' + fresh.length + ' fresh')

phase('Verify')
const { take, over } = capClaims(fresh, VERIFY_CAP)
if (over.length) log('verify cap: verified ' + take.length + '/' + fresh.length + ', ' + over.length + ' over cap -> UNVERIFIED')
const judged = (await parallel(take.map(f => () =>
  parallel(Array.from({ length: flags.votes }, (_, v) => () =>
    agent('Skeptic ' + (v + 1) + ': real ' + (f.lens || '') + ' issue or false positive? Default real=false unless you confirm by reading the code.\n' + f.file + ':' + f.line + ' - ' + f.desc,
      { label: 'verify:' + f.file, phase: 'Verify', agentType: SKEPTIC, schema: VERDICT })))
    .then(votes => {
      const vv = votes.filter(Boolean)
      return { ...f, verdict: tallyVotes(vv, flags.votes), failed: flags.votes - vv.length }
    })))).filter(Boolean)
const confirmed = judged.filter(f => f.verdict === 'CONFIRMED')
const unverified = judged.filter(f => f.verdict === 'UNVERIFIED').concat(over.map(f => ({ ...f, verdict: 'UNVERIFIED' })))

phase('Synthesize')
const report = await agent(
  'Apply the synthesis discipline in ~/.claude/workflows/partials/SYNTHESIS.md (read it first).\n' +
  'Assemble a p0/p1/p2 fix list for ' + flags.repo + '. CONFIRMED findings (survived a ' + flags.votes + '-vote refutation):\n' +
  JSON.stringify(confirmed, null, 2) + '\n' +
  'Order p0 first. Each gets a one-line fix and the recommended test mechanism.',
  { label: 'synthesize', phase: 'Synthesize' })

const tally = {
  found: found.length, fresh: fresh.length, confirmed: confirmed.length,
  refuted: judged.filter(f => f.verdict === 'REFUTED').length,
  unverified: unverified.length,
  failedVerifiers: judged.reduce((a, f) => a + (f.failed || 0), 0),
  overCap: over.length,
}
return { repo: flags.repo, lang: flags.lang, lenses, tally, confirmed, report }
