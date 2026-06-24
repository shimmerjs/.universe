export const meta = {
  name: 'aw-implement',
  description: '[spec= verify=3 review=on isolation=inplace intensity=5 subagents=custom|stock] Spec-driven execution: lock a written spec, implement it in a fresh subagent seeded ONLY by the spec, build/verify, then adversarially review the diff. Informal word=value flags (long or short, anywhere in the prompt).',
  whenToUse: 'Executing a well-scoped change end-to-end; tune spec, verify, review, isolation',
  phases: [{ title: 'Spec' }, { title: 'Execute' }, { title: 'Verify' }, { title: 'Review' }],
  flags: {
    spec:      { short: 'e', type: 'str', default: '', help: 'path to a written spec/design doc; else the prompt is the task' },
    verify:    { short: 'v', type: 'int', default: 3, min: 1, max: 5, help: 'skeptics judging the diff against the spec' },
    review:    { short: 'r', type: 'str', default: 'on', choices: ['on', 'off'], help: 'adversarial correctness review of the diff' },
    isolation: { short: 'n', type: 'str', default: 'inplace', choices: ['inplace', 'worktree'], help: 'where execution writes' },
    intensity: { short: 'i', type: 'int', default: 5, min: 0, max: 10, help: 'one knob scaling the unset verify quorum' },
    subagents: { short: 's', type: 'str', default: 'custom', choices: ['custom', 'stock'], help: 'stock drops the custom agent types' },
  },
}

// Examples:
//   implement add a --json flag to the status command, grounded in cmd/status
//   implement spec=docs/specs/cache.md verify=4
//   implement isolation=worktree refactor the signing path per the locked spec

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

const fromIntensity = (i) => { i = Math.max(0, Math.min(10, i)); return {
  votes: i <= 1 ? 1 : i <= 4 ? 2 : i <= 7 ? 3 : i <= 9 ? 4 : 5,
} }
if (set.has('intensity')) {
  const k = fromIntensity(flags.intensity)
  // intensity scales only the verify quorum -- this workflow's one numeric knob.
  if (!set.has('verify')) flags.verify = k.votes
}

const stock = flags.subagents === 'stock'
const DESIGNER = stock ? undefined : 'designer'
const REVIEWER = stock ? undefined : 'reviewer'
const SKEPTIC  = stock ? undefined : 'skeptic'
const iso = flags.isolation === 'worktree' ? 'worktree' : undefined

if (!prompt && !flags.spec) { log('no task or spec=path given -- nothing to implement'); return }
log('implement: ' + (flags.spec ? 'spec=' + flags.spec : 'task from prompt') + ' verify=' + flags.verify + ' review=' + flags.review + ' isolation=' + flags.isolation)

// Phase 1: lock a written spec -- the execution anchor (DESIGN_DOCTRINE: the spec
// is what execution re-reads and implements against, not this thread).
phase('Spec')
const SPEC = { type: 'object', required: ['spec', 'acceptance'], properties: {
  spec: { type: 'string' },
  files: { type: 'array', items: { type: 'string' } },
  acceptance: { type: 'array', items: { type: 'string' } },   // build/test commands that must pass
  risks: { type: 'array', items: { type: 'string' } } } }
const spec = flags.spec
  ? await agent('Read the spec at ' + flags.spec + '. Restate it as a concrete implementation spec: exact files/symbols to change, the target state, acceptance criteria (the build/test commands that must pass), and known risks. Do not design anew -- faithfully tighten what is written.',
      { label: 'spec', phase: 'Spec', agentType: DESIGNER, schema: SPEC })
  : await agent('Produce a concrete implementation spec for this task: ' + prompt + '\nGround it in the real repo (cite files at path:line). State the target state exactly (files/symbols), the migration path, the acceptance criteria (build/test commands that must pass), and what gets harder. One approach, not a menu.',
      { label: 'spec', phase: 'Spec', agentType: DESIGNER, schema: SPEC })
log('spec locked: ' + (spec.files || []).length + ' files, ' + (spec.acceptance || []).length + ' acceptance criteria')

// Phase 2: fork execution into a fresh subagent seeded ONLY by the spec.
phase('Execute')
const EXEC = { type: 'object', required: ['summary', 'built'], properties: {
  summary: { type: 'string' },
  filesChanged: { type: 'array', items: { type: 'string' } },
  built: { type: 'boolean' },                                 // did the acceptance commands pass
  buildOutput: { type: 'string' },
  worktree: { type: 'string' } } }
const specText = 'SPEC (implement exactly this; if the spec itself is wrong, STOP and report -- do not freelance or expand scope):\n' + spec.spec +
  '\n\nFiles: ' + (spec.files || []).join(', ') +
  '\nAcceptance (must pass): ' + (spec.acceptance || []).join(' ; ')
const exec = await agent(specText +
  '\n\nImplement the spec. Match the surrounding code. After editing, run the acceptance build/test commands and report whether they pass, with the real output (not an assumption). Stay within the spec.' +
  (iso ? ' You are in an isolated git worktree -- report its absolute path.' : ' Report the files you changed.'),
  { label: 'execute', phase: 'Execute', isolation: iso, schema: EXEC })
log('execute: ' + (exec.filesChanged || []).length + ' files changed, acceptance ' + (exec.built ? 'PASS' : 'FAIL/unrun'))

// Phase 3: refute-default skeptics judge the diff against the spec's acceptance.
phase('Verify')
const VERDICT = { type: 'object', required: ['meetsSpec'], properties: {
  meetsSpec: { type: 'boolean' }, gaps: { type: 'array', items: { type: 'string' } }, why: { type: 'string' } } }
const where = exec.worktree ? '\nRead the diff in worktree: ' + exec.worktree : '\nRead the working-tree diff (git diff).'
const votes = (await parallel(Array.from({ length: flags.verify }, (_, i) => () =>
  agent('Skeptic ' + (i + 1) + ': does the implementation actually satisfy the spec and its acceptance criteria? Default meetsSpec=false unless you confirm by reading the real diff/code.' +
    '\nSPEC:\n' + spec.spec + '\nACCEPTANCE: ' + (spec.acceptance || []).join(' ; ') +
    '\nEXECUTION SUMMARY: ' + exec.summary + '\nFiles: ' + (exec.filesChanged || []).join(', ') + where,
    { label: 'verify:' + (i + 1), phase: 'Verify', agentType: SKEPTIC, schema: VERDICT }))) ).filter(Boolean)
const quorum = Math.ceil(flags.verify / 2)
const meets = votes.filter(v => v.meetsSpec).length
// sub-quorum (skeptics crashed) -> UNVERIFIED; ties -> GAPS (needs a majority to clear).
const specVerdict = votes.length < quorum ? 'UNVERIFIED' : (meets > flags.verify / 2 ? 'MEETS' : 'GAPS')
const gaps = votes.flatMap(v => v.gaps || [])
log('verify: ' + specVerdict + ' (' + meets + '/' + votes.length + ' meets-spec)')

// Phase 4: adversarial correctness review of the diff, unless review=off.
let review = null
if (flags.review !== 'off') {
  phase('Review')
  review = await agent('Review the diff this change introduced for correctness / security / perf bugs (file:line, concrete, no style nits). Apply ~/.claude/workflows/partials/SYNTHESIS.md: confirmed-only, name the trade-off, one reconciled verdict.' +
    '\nEXECUTION SUMMARY: ' + exec.summary + '\nFiles: ' + (exec.filesChanged || []).join(', ') + where,
    { label: 'review', phase: 'Review', agentType: REVIEWER })
}

return {
  task: prompt || flags.spec,
  spec: spec.spec,
  acceptance: spec.acceptance,
  risks: spec.risks || [],
  built: exec.built,
  filesChanged: exec.filesChanged || [],
  worktree: exec.worktree || null,
  specVerdict,
  gaps,
  review,
  note: iso
    ? 'Execution ran in an isolated worktree -- review the diff there and merge if good.'
    : 'Execution edited the working tree in place; the Stop go-check gate still applies on turn end.',
}
