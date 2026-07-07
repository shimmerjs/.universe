// Build-time cheatsheet extractor. Reads every aw-*.js in <workflowsDir>, slices
// the pure-literal `meta` block AND the body-level `const FLAGS` block (each
// closing brace sits at column 0, so a non-greedy match to the first
// newline-then-brace captures the whole literal), paren-wraps and evals them,
// scrapes the `// Examples:` comment block, and emits one cheatsheet.json.
// Single source of truth = each workflow's `const FLAGS` (outside meta: the
// Workflow runtime strips the meta export, so bodies can't read meta.flags),
// so the cheatsheet can never drift from what parseFlags actually accepts.
// Not deployed (named .mjs so workflows/default.nix's *.js deploy glob ignores
// it); run only by cheatsheet.nix at build time:
//   node cheatsheet-gen.mjs <workflowsDir> > out.json
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const dir = process.argv[2]
if (!dir) { console.error('usage: cheatsheet-gen.mjs <workflowsDir>'); process.exit(2) }

const renderDefault = (d) => {
  if (d == null) return ''
  if (Array.isArray(d)) return d.join(',')
  if (typeof d === 'object') {
    if (Array.isArray(d.list)) return d.list.join(',')
    if (d.count != null) return String(d.count)
    return JSON.stringify(d)
  }
  return String(d)
}

const workflows = []
for (const f of readdirSync(dir).filter((f) => /^aw-.*\.js$/.test(f)).sort()) {
  const src = readFileSync(join(dir, f), 'utf8')
  const m = src.match(/export const meta = (\{[\s\S]*?\n\})/)
  if (!m) { console.error(`${f}: no extractable meta literal (closing brace must be at col 0)`); process.exit(2) }
  let meta
  try { meta = (0, eval)('(' + m[1] + ')') }
  catch (e) { console.error(`${f}: meta eval failed: ${e.message}`); process.exit(2) }

  const examples = []
  const em = src.match(/\/\/ Examples:\n((?:\/\/.*\n)+)/)
  if (em) for (const line of em[1].split('\n')) {
    const t = line.replace(/^\/\/\s?/, '').trim()
    if (t) examples.push(t)
  }

  let flagSpec = meta.flags || {}
  const fm = src.match(/\nconst FLAGS = (\{[\s\S]*?\n\})/)
  if (fm) {
    try { flagSpec = (0, eval)('(' + fm[1] + ')') }
    catch (e) { console.error(`${f}: FLAGS eval failed: ${e.message}`); process.exit(2) }
  }

  const flags = []
  for (const [name, s] of Object.entries(flagSpec)) {
    let range = ''
    if (s.min != null || s.max != null) range = `${s.min ?? ''}..${s.max ?? ''}`
    else if (Array.isArray(s.choices)) range = s.choices.join('|')
    flags.push({ name, short: s.short || '', type: s.type || 'str', default: renderDefault(s.default), range, help: s.help || '' })
  }

  workflows.push({
    name: meta.name,
    description: (meta.description || '').replace(/^\[[^\]]*\]\s*/, ''),
    whenToUse: meta.whenToUse || '',
    phases: (meta.phases || []).map((p) => p.title),
    flags,
    examples,
  })
}
process.stdout.write(JSON.stringify({ workflows }, null, 2) + '\n')
