export const meta = {
  name: 'aw-audit',
  description: '[repo=. lang=go lenses=bug,test-gap,perf votes=3 intensity=5 subagents=custom|stock] Adversarially audit an existing implementation across discovered packages for bugs/test-gaps/perf; refute-verify each; emit a p0/p1/p2 fix list. word=value flags.',
  whenToUse: 'Auditing a rebuilt component; tune repo, lang, lenses, votes',
  phases: [{ title: 'Map' }, { title: 'Audit' }, { title: 'Verify' }, { title: 'Synthesize' }],
}

// Examples:
//   audit repo=. lang=go lenses=bug,test-gap,perf votes=5

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
  repo: { type: 'str', default: '.' },
  lang: { type: 'str', default: 'go' },
  lenses: { type: 'axes', default: { list: ['bug', 'test-gap', 'perf'] } },
  votes: { type: 'int', default: 3, min: 1, max: 5 },
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

// Phase 0: discover the package map (never hardcode it).
phase('Map')
const MAP = { type: 'object', required: ['packages'], properties: { packages: { type: 'array', items: { type: 'string' } } } }
const map = await agent(
  'Map the package/source layout of ' + flags.repo + ' (lang ' + flags.lang + '). Use tree/glob/git ls-files - do not hardcode. Return the packages worth auditing.',
  { label: 'map', phase: 'Map', agentType: REVIEWER, schema: MAP })

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
const judged = (await parallel(fresh.map(f => () =>
  parallel(Array.from({ length: flags.votes }, (_, v) => () =>
    agent('Skeptic ' + (v + 1) + ': real ' + (f.lens || '') + ' issue or false positive? Default real=false unless you confirm by reading the code.\n' + f.file + ':' + f.line + ' - ' + f.desc,
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
  'Assemble a p0/p1/p2 fix list for ' + flags.repo + '. CONFIRMED findings (survived a ' + flags.votes + '-vote refutation):\n' +
  JSON.stringify(confirmed, null, 2) + '\n' +
  'Order p0 first. Each gets a one-line fix and the recommended test mechanism.',
  { label: 'synthesize', phase: 'Synthesize' })

const tally = {
  found: found.length, fresh: fresh.length, confirmed: confirmed.length,
  refuted: judged.filter(f => f.verdict === 'REFUTED').length,
  unverified: unverified.length,
  failedVerifiers: judged.reduce((a, f) => a + (f.failed || 0), 0),
}
return { repo: flags.repo, lang: flags.lang, lenses, tally, confirmed, report }
