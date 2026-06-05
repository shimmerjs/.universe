export const meta = {
  name: 'understand',
  description: 'Fan out read-only mappers over subsystem streams, each emitting a goal-relative summary, then synthesize a dependency-ordered plan flagging shared touch-points and human decisions. word=value flags.',
  whenToUse: 'Mapping a subsystem before committing; tune root, pivot, streams, depth',
  phases: [{ title: 'Slice' }, { title: 'Map' }, { title: 'Synthesize' }],
}

// Examples:
//   understand root=internal/can pivot=remote-execution
//   understand root=. streams=prepare,exec,resolve depth=deep

// ── informal flags: <word>=<value> up front, then the optional pivot prompt. ──
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
  const text = (typeof raw === 'string' ? raw : (raw && raw.prompt) || '').trim()
  const toks = text.length ? text.split(/\s+/) : []
  let i = 0
  for (; i < toks.length; i++) {
    const m = /^([A-Za-z][A-Za-z0-9_-]*)=(.*)$/.exec(toks[i])
    if (!m || !(m[1] in spec)) break
    flags[m[1]] = coerce(m[2], spec[m[1]])
  }
  return { flags, prompt: toks.slice(i).join(' ') }
}

const { flags, prompt } = parseFlags(args, {
  root: { type: 'str', default: '.' },
  pivot: { type: 'str', default: '' },              // the north-star subject; relevance is measured against it
  streams: { type: 'axes', default: { count: 4 } }, // slices to map, or N to auto-discover
  depth: { type: 'str', default: 'structural' },    // structural | deep
  priorart: { type: 'str', default: '' },           // priorart=on: fold in how the field builds this kind of subsystem
})

const MAPPER = 'mapper'
const pivot = flags.pivot || prompt || 'the subsystem as a whole'

// Phase 0: discover the slices to map (never hardcode them).
phase('Slice')
let streams = flags.streams.list
if (!streams) {
  const S = { type: 'object', required: ['streams'], properties: { streams: { type: 'array', items: { type: 'string' } } } }
  streams = (await agent(
    'Discover ' + flags.streams.count + ' orthogonal slices of ' + flags.root + ' worth mapping, relative to the pivot "' + pivot + '". Use tree/git ls-files; do not hardcode.',
    { label: 'slice', phase: 'Slice', agentType: MAPPER, schema: S })).streams
}
log('understand: root=' + flags.root + ' pivot=' + pivot + ' streams=' + streams.join('+') + ' depth=' + flags.depth)

const MAP = { type: 'object', required: ['stream', 'currentState', 'gaps', 'keyFiles', 'relevance'], properties: {
  stream: { type: 'string' }, currentState: { type: 'string' }, gaps: { type: 'array', items: { type: 'string' } },
  keyFiles: { type: 'array', items: { type: 'string' } }, relevance: { type: 'string' }, undetermined: { type: 'string' } } }

phase('Map')
const rawMaps = await parallel(streams.map(s => () => agent(
  'Map the "' + s + '" slice of ' + flags.root + ' (' + flags.depth + ' depth), relative to the pivot "' + pivot + '".\n' +
  'Report currentState, gaps, keyFiles (path:line), relevance to the pivot, and what you could NOT determine. Read-only.',
  { label: 'map:' + s, phase: 'Map', agentType: MAPPER, schema: MAP })))
const maps = rawMaps.filter(Boolean)
const failedStreams = streams.filter((s, i) => !rawMaps[i])   // index-aligned: which streams dropped
log('understand: mapped ' + maps.length + '/' + streams.length + ' streams')
if (failedStreams.length) log('WARNING: streams that failed to map: ' + failedStreams.join(', ') + ' -- plan rests on partial coverage')

// optional outward bend: fold in how the field builds this kind of subsystem (composes the prior-art workflow).
let priorArt = ''
if (flags.priorart) {
  try {
    const pa = await workflow('prior-art', 'verify-scope=load-bearing how is this kind of subsystem built elsewhere: ' + pivot)
    priorArt = (pa && pa.report) || ''
    log('understand: folded in a prior-art pass')
  } catch (e) { log('prior-art bend skipped (cannot nest workflows): ' + e) }
}

phase('Synthesize')
const plan = await agent(
  'Synthesize a dependency-ordered plan for working toward "' + pivot + '" in ' + flags.root + ', from these stream maps:\n' +
  JSON.stringify(maps, null, 2) + '\n' +
  (failedStreams.length ? 'STREAMS THAT FAILED TO MAP (the plan rests on partial coverage - flag this explicitly): ' + failedStreams.join(', ') + '\n' : '') +
  (priorArt ? 'PRIOR ART (how the field builds this kind of subsystem - weigh the plan against it):\n' + priorArt + '\n' : '') +
  'Order by dependency. Flag shared files/seams that multiple streams touch (coordination risk) and the human decisions that must be made. ' +
  'Surface what each map could not determine; do not paper over gaps. End with the single next action.',
  { label: 'synthesize', phase: 'Synthesize' })

return { root: flags.root, pivot, streams, mapped: maps.length, failed: failedStreams, maps, plan }
