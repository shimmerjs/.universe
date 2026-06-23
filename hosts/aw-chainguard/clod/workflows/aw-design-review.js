export const meta = {
  name: 'aw-design-review',
  description: '[design= code-root=. lenses=feasibility,semantics,scale,hermeticity,ordering locked= votes=3 priorart= intensity=5 subagents=custom|stock] Stress-test a design through heterogeneous lenses with a refute-default verifier; propose a candidate if given a problem; reconcile confirmed flaws into a prioritized change list. word=value flags.',
  whenToUse: 'Reviewing a design doc or a design problem; tune design, code-root, lenses, locked',
  phases: [{ title: 'Frame' }, { title: 'Critique' }, { title: 'Verify' }, { title: 'Synthesize' }],
}

// Examples:
//   design-review design=docs/DESIGN.md code-root=internal/can
//   design-review code-root=internal/gaffe lenses=feasibility,scale should gaffe materialize lazily?

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
  design: { type: 'str', default: '' },                          // doc path OR a problem statement (falls back to the prompt)
  'code-root': { type: 'str', default: '.' },
  lenses: { type: 'axes', default: { list: ['feasibility', 'semantics', 'scale', 'hermeticity', 'ordering'] } },
  locked: { type: 'str', default: '' },                          // path to a CONSTRAINTS / LOCKED block
  votes: { type: 'int', default: 3, min: 1, max: 5 },            // skeptics per flaw (a fatal veto must not rest on one vote)
  priorart: { type: 'str', default: '' },                        // priorart=on: fold a prior-art pass into the critique
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
  // intensity scales votes (and the lenses count, below) -- this workflow's only knobs.
  if (!set.has('votes')) flags.votes = k.votes
  if (!set.has('lenses') && flags.lenses && flags.lenses.count != null) flags.lenses = { count: k.fanout }
}
const stock = flags.subagents === 'stock'

const DESIGNER = stock ? undefined : 'designer'
const SKEPTIC = stock ? undefined : 'skeptic'

const designInput = flags.design || prompt || ''
if (!designInput) { log('no design doc or problem given'); return }

// Phase 0: load the LOCKED constraints; if design is a problem (not a doc), propose a candidate to review.
phase('Frame')
let locked = ''
if (flags.locked) {
  const LK = { type: 'object', required: ['text'], properties: { text: { type: 'string' } } }
  locked = (await agent('Read the constraints file ' + flags.locked + ' and return its text verbatim as the LOCKED block.',
    { label: 'locked', phase: 'Frame', agentType: DESIGNER, schema: LK })).text
}
const FRAME = { type: 'object', required: ['kind', 'design'], properties: {
  kind: { type: 'string', enum: ['doc', 'proposed'] }, design: { type: 'string' } } }
const looksLikePath = /[/.]/.test(designInput) && !designInput.includes(' ')
const framed = await agent(
  (looksLikePath
    ? 'Read the design at ' + designInput + ' and summarize the approach to be reviewed (kind=doc).'
    : 'This is a design PROBLEM, not a doc: "' + designInput + '". Propose ONE concrete candidate approach to review (kind=proposed), grounded in ' + flags['code-root'] + '.') +
  (locked ? '\nLOCKED constraints (do not violate or relitigate):\n' + locked : ''),
  { label: 'frame', phase: 'Frame', agentType: DESIGNER, schema: FRAME })

let lenses = flags.lenses.list
if (!lenses) {
  const L = { type: 'object', required: ['lenses'], properties: { lenses: { type: 'array', items: { type: 'string' } } } }
  lenses = (await agent('Propose ' + flags.lenses.count + ' orthogonal stress-test lenses for this design:\n' + framed.design,
    { label: 'derive-lenses', phase: 'Frame', agentType: DESIGNER, schema: L })).lenses
}
log('design-review: ' + framed.kind + ' lenses=' + lenses.join('+') + (locked ? ' (locked)' : ''))

// optional outward bend: fold in how the field solves this problem (composes the prior-art workflow).
let priorArt = ''
if (flags.priorart) {
  try {
    const pa = await workflow('prior-art', 'verify-scope=load-bearing how does the field solve this design problem: ' + designInput)
    priorArt = (pa && pa.report) || ''
    log('design-review: folded in a prior-art pass')
  } catch (e) { log('prior-art bend skipped (cannot nest workflows): ' + e) }
}

const FLAW = { type: 'object', required: ['flaws'], properties: { flaws: { type: 'array', items: {
  type: 'object', required: ['lens', 'desc', 'severity'], properties: {
    lens: { type: 'string' }, desc: { type: 'string' }, evidence: { type: 'string' },
    severity: { type: 'string', enum: ['fatal', 'major', 'minor'] } } } } } }
const VERDICT = { type: 'object', required: ['real'], properties: { real: { type: 'boolean' }, why: { type: 'string' } } }

phase('Critique')
const found = (await parallel(lenses.map(lens => () => agent(
  'Stress-test this design through the ' + lens + ' lens. Ground every flaw in real code under ' + flags['code-root'] + ' (file:line).\n' +
  'Design:\n' + framed.design + (locked ? '\nLOCKED (do not flag these as flaws):\n' + locked : ''),
  { label: 'critique:' + lens, phase: 'Critique', agentType: DESIGNER, schema: FLAW }))))
  .filter(Boolean).flatMap(r => r.flaws || [])
const seen = new Set()
const fresh = found.filter(f => {
  const k = (f.lens + ':' + (f.desc || '')).toLowerCase()
  if (seen.has(k)) return false; seen.add(k); return true
})
log('design-review: ' + found.length + ' flaws, ' + fresh.length + ' fresh')

phase('Verify')
const quorum = Math.ceil(flags.votes / 2)
const judged = (await parallel(fresh.map(f => () =>
  parallel(Array.from({ length: flags.votes }, (_, v) => () =>
    agent('Skeptic ' + (v + 1) + ': is this a REAL flaw in the design, or a misread? Default real=false unless you confirm against the code/design. Check it is not a LOCKED choice.\n' +
      f.lens + ': ' + f.desc + '\nEvidence: ' + (f.evidence || 'none'),
      { label: 'verify:' + f.lens, phase: 'Verify', agentType: SKEPTIC, schema: VERDICT })))
    .then(votes => {
      const vv = votes.filter(Boolean)
      const verdict = vv.length < quorum ? 'UNVERIFIED'
        : (vv.filter(x => x.real).length > flags.votes / 2 ? 'CONFIRMED' : 'REFUTED')
      return { ...f, verdict }
    })))).filter(Boolean)
const confirmed = judged.filter(f => f.verdict === 'CONFIRMED')
const unverified = judged.filter(f => f.verdict === 'UNVERIFIED')

phase('Synthesize')
const fatal = confirmed.some(f => f.severity === 'fatal')
const report = await agent(
  'Apply the synthesis discipline in ~/.claude/workflows/partials/SYNTHESIS.md and the design doctrine in ~/.claude/workflows/partials/DESIGN_DOCTRINE.md (read both first).\n' +
  'Synthesize a design review. CONFIRMED flaws (verified against code):\n' + JSON.stringify(confirmed, null, 2) + '\n' +
  (priorArt ? 'PRIOR ART on this problem (weigh the design against how the field solves it):\n' + priorArt + '\n' : '') +
  'A single fatal flaw vetoes the design regardless of the rest (there ' + (fatal ? 'IS' : 'is NO') + ' confirmed fatal flaw). ' +
  'Output a prioritized change list (fatal, then major, then minor), each grounded in file:line, ending in one go/no-go.',
  { label: 'synthesize', phase: 'Synthesize' })

return { kind: framed.kind, lenses, confirmed: confirmed.length, unverified: unverified.length, fatal, report }
