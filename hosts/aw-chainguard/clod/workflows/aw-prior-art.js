export const meta = {
  name: 'aw-prior-art',
  description: '[areas=5 verify-scope=load-bearing intensity=5 subagents=custom|stock] Fan out deep-dives over prior-art sources (local/dep/web), refute-verify the load-bearing claims, synthesize a cited report with a corrections section. word=value flags; the prompt is the question.',
  whenToUse: 'Researching how others solve X; tune areas, verify-scope',
  phases: [{ title: 'Plan' }, { title: 'Dig' }, { title: 'Verify' }, { title: 'Synthesize' }],
}

// Examples:
//   prior-art how do build systems model remote CAS GC
//   prior-art areas=bazel,buck2,nix verify-scope=load-bearing hermetic go test

// ── informal flags: <word>=<value> up front, then the research question. ──
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
  areas: { type: 'axes', default: { count: 5 } },                // investigation areas, or N to auto-derive
  'verify-scope': { type: 'str', default: 'load-bearing' },      // all | load-bearing | none
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
  if (!set.has('areas') && flags.areas && flags.areas.count != null) flags.areas = { count: k.fanout }
}
const stock = flags.subagents === 'stock'

const RESEARCHER = stock ? undefined : 'researcher'
const SKEPTIC = stock ? undefined : 'skeptic'
if (!prompt) { log('no research question given after the flags'); return }

phase('Plan')
let areas = flags.areas.list
if (!areas) {
  const A = { type: 'object', required: ['areas'], properties: { areas: { type: 'array', items: {
    type: 'object', required: ['area', 'source'], properties: {
      area: { type: 'string' }, source: { type: 'string', enum: ['local', 'dep', 'web'] } } } } } }
  const planned = (await agent('Plan ' + flags.areas.count + ' distinct investigation areas for: ' + prompt + '. Tag each source local|dep|web.',
    { label: 'plan', phase: 'Plan', agentType: RESEARCHER, schema: A })).areas
  areas = planned.map(a => a.area + ' [' + a.source + ']')
}
log('prior-art: areas=' + areas.length + ' verify=' + flags['verify-scope'])

const CLAIM = { type: 'object', required: ['claims'], properties: { claims: { type: 'array', items: {
  type: 'object', required: ['text'], properties: {
    text: { type: 'string' }, source: { type: 'string' }, loadBearing: { type: 'boolean' } } } } } }
const VERDICT = { type: 'object', required: ['refuted'], properties: { refuted: { type: 'boolean' }, correction: { type: 'string' } } }

phase('Dig')
const found = (await parallel(areas.map(a => () => agent(
  'Deep-dive prior art for "' + prompt + '", area: ' + a + '. Prefer primary sources; cite each claim with a source. Mark load-bearing claims.',
  { label: 'dig:' + a.slice(0, 24), phase: 'Dig', agentType: RESEARCHER, schema: CLAIM }))))
  .filter(Boolean).flatMap(r => r.claims || [])
const seen = new Set()
const fresh = found.filter(c => {
  const k = (c.text || '').slice(0, 80).toLowerCase()
  if (seen.has(k)) return false; seen.add(k); return true
})
log('prior-art: ' + found.length + ' claims, ' + fresh.length + ' fresh')

phase('Verify')
const corrections = []
let verified = fresh
let unverifiedCount = 0
if (flags['verify-scope'] !== 'none') {
  const targets = flags['verify-scope'] === 'all' ? fresh : fresh.filter(c => c.loadBearing)
  const judged = await parallel(targets.map(c => () =>
    agent('Skeptic: try to REFUTE this prior-art claim. Default refuted=true if you cannot confirm from a primary source. If it is partly wrong, give a correction.\nClaim: ' + c.text + '\nSource: ' + (c.source || 'none'),
      { label: 'verify:' + (c.text || '').slice(0, 24), phase: 'Verify', agentType: SKEPTIC, schema: VERDICT })
      .then(v => ({ claim: c, v }))
      .catch(() => ({ claim: c, v: null }))))
  const refutedKeys = new Set(), unverifiedKeys = new Set()
  for (const j of judged) {
    if (!j) continue
    const key = (j.claim.text || '').slice(0, 80).toLowerCase()
    if (!j.v) { unverifiedKeys.add(key); continue }   // verifier crashed -> UNVERIFIED, not kept-as-verified
    if (j.v.refuted) refutedKeys.add(key)
    if (j.v.correction) corrections.push({ claim: j.claim.text, correction: j.v.correction })
  }
  verified = fresh.filter(c => { const k = (c.text || '').slice(0, 80).toLowerCase(); return !refutedKeys.has(k) && !unverifiedKeys.has(k) })
  unverifiedCount = unverifiedKeys.size
  log('prior-art: verified ' + targets.length + ', refuted ' + refutedKeys.size + ', unverified ' + unverifiedKeys.size + ', ' + corrections.length + ' corrections')
}

phase('Synthesize')
const report = await agent(
  'Write a cited report answering: ' + prompt + '\nUse these verified claims:\n' +
  verified.map((c, i) => '[' + (i + 1) + '] ' + c.text + ' (' + (c.source || 'unsourced') + ')').join('\n') +
  '\nEnd with a "Corrections from verification" section listing:\n' + JSON.stringify(corrections, null, 2),
  { label: 'synthesize', phase: 'Synthesize' })

return { question: prompt, areas, verified: verified.length, unverified: unverifiedCount, corrections: corrections.length, report }
