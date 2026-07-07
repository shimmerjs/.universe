// Fixture unit tests for the aw-* workflow scripts. Textually extracts the
// FLAGS literal and every body-level `function name(...) {...}` helper (same
// newline-then-closing-brace-at-col-0 anchor as workflow-lint.py and
// cheatsheet-gen.mjs), assembles them with new Function, and asserts against
// the JSON fixtures in $fixturesDir. Plain node, no npm deps.
//
// Fixture shapes:
//   { script, kind: 'parse',  args, expect: { flags: {...subset...}, prompt, errors } }
//   { script, kind: 'helper', helper, input: [...], expect }
// A helper input of { "$set": [...] } revives to a Set (JSON cannot encode one).
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import assert from 'node:assert/strict'

const wfDir = process.env.workflowsDir
const fxDir = process.env.fixturesDir
if (!wfDir || !fxDir) { console.error('workflowsDir and fixturesDir env vars are required'); process.exit(2) }

const FLAGS_RE = /\nconst FLAGS = (\{[\s\S]*?\n\})/
const FN_RE = /\nfunction ([A-Za-z_$][A-Za-z0-9_$]*)\([^)]*\) \{[\s\S]*?\n\}/g

const cache = {}
function load(script) {
  if (cache[script]) return cache[script]
  const src = readFileSync(join(wfDir, script), 'utf8')
  const flagsM = src.match(FLAGS_RE)
  const fns = [...src.matchAll(FN_RE)]
  if (!fns.length) throw new Error(script + ': no extractable functions (closing brace must be at col 0)')
  const body = fns.map(m => m[0]).join('\n') +
    '\nreturn { ' + fns.map(m => m[1]).join(', ') + (flagsM ? ', FLAGS: ' + flagsM[1] : '') + ' }'
  return (cache[script] = new Function(body)())
}

const revive = (x) =>
  (x && typeof x === 'object' && !Array.isArray(x) && Array.isArray(x.$set) && Object.keys(x).length === 1)
    ? new Set(x.$set) : x

let pass = 0, fail = 0
for (const file of readdirSync(fxDir).filter(f => f.endsWith('.json')).sort()) {
  const fx = JSON.parse(readFileSync(join(fxDir, file), 'utf8'))
  try {
    const mod = load(fx.script)
    if (fx.kind === 'parse') {
      if (typeof mod.parseFlags !== 'function' || !mod.FLAGS) throw new Error(fx.script + ': parseFlags/FLAGS not extractable')
      const got = mod.parseFlags(fx.args, mod.FLAGS)
      for (const k of Object.keys(fx.expect.flags || {}))
        assert.deepStrictEqual(got.flags[k], fx.expect.flags[k], 'flag ' + k)
      assert.strictEqual(got.prompt, fx.expect.prompt, 'prompt')
      assert.deepStrictEqual(got.errors, fx.expect.errors, 'errors')
    } else if (fx.kind === 'helper') {
      if (typeof mod[fx.helper] !== 'function') throw new Error(fx.script + ': helper ' + fx.helper + ' not extractable')
      assert.deepStrictEqual(mod[fx.helper](...fx.input.map(revive)), fx.expect)
    } else {
      throw new Error('unknown fixture kind: ' + fx.kind)
    }
    pass++
    console.log('PASS ' + file)
  } catch (e) {
    fail++
    console.error('FAIL ' + file + ': ' + (e && e.message))
  }
}
console.log(pass + ' passed, ' + fail + ' failed')
if (fail || !pass) process.exit(1)
