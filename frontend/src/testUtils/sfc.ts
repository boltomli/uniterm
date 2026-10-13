// Source-level helpers for wiring tests.
//
// The frontend has no component-mount test infrastructure (no jsdom, no
// @vue/test-utils — the default vitest environment is node), so regressions
// that live inside SFC lifecycle hooks are guarded by reading the SFC source
// and asserting the hook bodies call the extracted helpers they must call.
// Same pattern as types/settings.schema.test.ts, which reads the Go store
// source to keep the two sides in sync.
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
/** `frontend/src` — all paths passed to readSrc are relative to it. */
export const SRC_ROOT = resolve(here, '..')

export function readSrc(relPathFromSrc: string): string {
  return readFileSync(resolve(SRC_ROOT, relPathFromSrc), 'utf-8')
}

/** Content of the first `<script setup ...>` block of an SFC. */
export function scriptBody(src: string): string {
  const open = src.search(/<script\b[^>]*>/)
  if (open < 0) return ''
  const start = src.indexOf('>', open) + 1
  const end = src.indexOf('</script>', start)
  return end < 0 ? src.slice(start) : src.slice(start, end)
}

/** Arguments of every `hook(...)` call in the source (an arrow body, or a
 *  direct reference such as `onUnmounted(stopRebind)`), paren-balanced. */
export function hookBodies(src: string, hook: string): string[] {
  const bodies: string[] = []
  let from = 0
  for (;;) {
    const start = src.indexOf(`${hook}(`, from)
    if (start < 0) return bodies
    const open = start + hook.length + 1
    // Start inside the call's parentheses: the argument list ends when its
    // nesting level returns to the call's own level.
    let depth = 1
    let end = -1
    for (let i = open; i < src.length; i += 1) {
      const ch = src[i]
      if (ch === '(' || ch === '{' || ch === '[') depth += 1
      else if (ch === ')' || ch === '}' || ch === ']') {
        depth -= 1
        if (depth === 0) {
          end = i
          break
        }
      }
    }
    if (end < 0) return bodies
    bodies.push(src.slice(open, end))
    from = end + 1
  }
}

/** Argument of the first `hook(...)` call. Empty string when the hook is
 *  absent. */
export function hookBody(src: string, hook: string): string {
  return hookBodies(src, hook)[0] ?? ''
}
