// Keyword highlight rule set + matcher shared by the overlay terminal
// highlighter (overlayHighlight.ts). Rules are plain regexes over rendered
// buffer text; a match maps to a HighlightCategory, which the renderer turns
// into a theme color via the xterm fork's cell color override API.
//
// Group 1, when present, is a consumed left word guard that is NOT part of
// the highlighted span (matchTextSpans trims it) — unless the rule sets
// noLeadTrim (e.g. the brace rule's group is a run anchor that IS part of
// the span). Right guards are zero-width lookaheads. No lookbehind anywhere
// — unsupported by the JavaScriptCore in macOS ≤12.3 WebView, where this
// module fails to parse.
export type HighlightCategory =
  | 'url' | 'host' | 'path' | 'datetime' | 'string'
  | 'success' | 'error' | 'warning' | 'info' | 'brace'
  | 'keyword' | 'ifname'

export interface HighlightRule {
  category: HighlightCategory
  noLeadTrim?: boolean
  regexes: RegExp[]
}

export const HIGHLIGHT_RULES: HighlightRule[] = [
  // Quoted strings first: a quoted run wins over any rule that would fire
  // inside it (IPs, URLs, timestamps…), matching editor-style precedence.
  // `*` (not `{2,}`) so EMPTY strings match too: a skipped `""` leaves both
  // quotes in the scan and the next match steals one, shifting every pair
  // after it — long strings further along then never match.
  { category: 'string',  regexes: [
    /"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/g,
  ]},
  { category: 'url',     regexes: [
    /https?:\/\/[A-Za-z0-9_.&?=%~#{}()@+-]+(?::?[A-Za-z0-9_./&?=%~#{}()@+-]+)?/gi,
  ]},
  // Network device config (Cisco IOS / Huawei VRP style; also matches the
  // config dumps of Linux net tools like iptables-save or FRR). Keywords are
  // multi-word command starters or distinctive single words; `interface`
  // only counts when an interface-name-like token follows, so prose stays
  // untouched.
  { category: 'keyword', regexes: [
    /(^|[^0-9a-z_&-])(interface(?=\s+(?:[a-z]*ethernet|vlan(?:if)?|eth-trunk|meth|loopback|port-channel|tunnel|console|null)\s*\d)|switchport(?:\s+(?:mode|access|trunk|native|voice))?|ip address|ipv6 address|dhcp (?:select|server|relay|enable|snooping)|port (?:link-type|trunk|hybrid|default|group)|mode lacp|access vlan|vlan batch|ip route-static|stp (?:enable|disable|mode|bpdu)|local-user|user-interface|sysname|link-type|trunk allow-pass)(?![a-z_-])/gi,
  ]},
  // Interface names: full vendor spellings plus Huawei abbreviations (GE…),
  // always name+digits+slash-groups so bare words like "ethernet" never hit.
  { category: 'ifname',  regexes: [
    /(^|[^0-9a-z_/-])((?:(?:twentyfivegigabit|hundredgigabit|fortygigabit|tengigabit|xgigabit|gigabit|fast)?ethernet|meth|(?:x?ge))\d+(?:\/\d+)+|vlanif\d+|vlan\d+|eth-trunk\d+|loopback\d+|port-channel\d+)(?![0-9a-z_.\-/])/gi,
  ]},
  { category: 'host',    regexes: [
    // IPv4 (first octet 1-254) and IPv6 (full and ::-compressed forms)
    /(^|[^0-9a-z_&-])(localhost|(?:1[0-9][0-9]|2[0-4][0-9]|25[0-4]|[1-9][0-9]|[1-9])\.\d+\.\d+\.\d+|null|none)(?![0-9a-z_-])/gi,
    /(^|[^0-9a-z_&-])((?:[a-f0-9]{1,4}:){7}[a-f0-9]{1,4}|(?:[a-f0-9]{1,4}:){1,7}:|(?:[a-f0-9]{1,4}:){1,6}:[a-f0-9]{1,4}|(?:[a-f0-9]{1,4}:){1,5}(?::[a-f0-9]{1,4}){1,2}|(?:[a-f0-9]{1,4}:){1,4}(?::[a-f0-9]{1,4}){1,3}|(?:[a-f0-9]{1,4}:){1,3}(?::[a-f0-9]{1,4}){1,4}|(?:[a-f0-9]{1,4}:){1,2}(?::[a-f0-9]{1,4}){1,5}|[a-f0-9]{1,4}:(?::[a-f0-9]{1,4}){1,6}|:(?::[a-f0-9]{1,4}){1,7})(?![0-9a-f:])/gi,
  ]},
  { category: 'error',   regexes: [
    // "<adjective> <noun>" phrases: bad address, invalid argument, …
    /(^|[^a-z_&-])((?:bad|wrong|incorrect|improper|invalid|unsupported)(?: file| memory)? (?:descriptor|alloc(?:ation)?|addr(?:ess)?|owner(?:ship)?|arg(?:ument)?|param(?:eter)?|setting|length|filename))(?![a-z_-])/gi,
    // denied, failed, segfault, no X found, …
    /(^|[^a-z_&-])((?:operation |connection |authentication |access |permission )?(?:denied|disallowed|not allowed|refused|problem|failed|failure|not permitted)|not properly|improperly|no [a-z]+(?: [a-z]+)? found|invalid|unsupported|not supported|seg(?:mentation )?fault|corrupt(?:ion|ed)?|overflow|underrun|not ok|unimplemented|unsuccessfull?|not implemented|permerrors?|fatal|critical|exceptions?|panic(?:ked|s)?|abort(?:ed|s|ing)?|errors?|crash(?:ed)?|core dump|\(ee\)|\(ni\))(?![a-z_-])/gi,
    // falsy output values ("=> no", "status: false")
    /([=>"':.,;({\[] *)(?:false|no|ko)(?=[\]=>"':.,;)} ]|$)/gi,
  ]},
  { category: 'success', regexes: [
    /(^|[^a-z_&-])(accepted|allowed|enabled|connected|successfully|successful|succeeded|success)(?![a-z_-])/gi,
  ]},
  { category: 'warning', regexes: [
    /(^|[^a-z_&-])(\[-w[a-z-]+\]|caught signal [0-9]+|cannot|not responding|(?:connection (?:to (?:remote host|[a-z0-9.]+) )?)?(?:closed|terminated|stopped)|exited|no more [a-z]+ available|unexpected|(?:command |binary |file )?not found|o{2,}ps|out of (?:space|memory)|low (?:memory|disk)|unknown|disabled|disconnect(?:ed|ion)?|deprecated|refused|cautions?|warns?(?:ed|ings?)?|\(ww\)|\(\?\?\)|could not|unable to)(?![a-z_-])/gi,
  ]},
  { category: 'info',    regexes: [
    /(^|[^a-z_&-])(last (?:failed )?login:|launching|checking|loading|creating|building|important|booting|starting|informational|informations?|info|notice|note|\(ii\)|\(\!\!\))(?![a-z_-])/gi,
  ]},
  // Character class includes `+`, `~`, `@` so paths like
  // `/usr/local/gcc-11.5.0/bin/g++` are recognised whole.
  { category: 'path',    regexes: [/(^|\s)(?:\/|~\/)[\w.+~@/-]+(?=[\s:;"')\]}]|$)/g] },
  { category: 'datetime', regexes: [
    /\b\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(?::\d{2})?(?:[.,]\d+)?Z?\b/g,
    /\b(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)\s+(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4}\b/g,
    /\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\b/g,
    // Left guard + lookahead keep MAC addresses (six colon-separated hex
    // pairs) out: an all-digit clock time must not sit inside a longer
    // hex:hex:… run. The guard group is trimmed as usual (see file header).
    /(^|[^0-9a-f:])\d{2}:\d{2}:\d{2}\b(?!(?::[0-9a-f]{2}){1,3})/gi,
  ]},
  // Consecutive identical symbols (`****`, `=====`, `>>>`) match once as a
  // whole run — the capture group is a backreference anchor, not a guard,
  // hence noLeadTrim.
  { category: 'brace',   noLeadTrim: true, regexes: [/([{}()\[\]|*=<>])\1*/g] },
]

export interface TextSpan {
  /** String index of the first character (inclusive). */
  start: number
  /** String index just past the last character (exclusive). */
  end: number
  category: HighlightCategory
}

export interface MatchTextOptions {
  /** Hard cap of accepted spans. */
  maxMatches?: number
  /** Pollable stop (e.g. a time budget). When it fires, `complete: false`
   * and the spans accepted so far are returned. */
  shouldStop?: () => boolean
}

export interface MatchTextResult {
  spans: TextSpan[]
  /** False when a cap or the stop probe ended the scan early. */
  complete: boolean
}

/** Run every rule regex over one line of plain text and collect the
 * non-overlapping highlight spans (first rule wins on overlap). Spans come
 * back sorted by position. The rule regexes carry `g` flags and are shared
 * across callers — safe because each call resets and exhausts lastIndex
 * synchronously. */
export function matchTextSpans(
  text: string,
  rules: HighlightRule[] = HIGHLIGHT_RULES,
  opts: MatchTextOptions = {},
): MatchTextResult {
  const maxMatches = opts.maxMatches ?? 40
  const shouldStop = opts.shouldStop
  const spans: TextSpan[] = []
  if (!text) return { spans, complete: true }
  const stopEarly = (): false => {
    spans.sort((a, b) => a.start - b.start)
    return false
  }
  // Occupied string positions prevent multi-rule overlap (first rule wins).
  const occupied = new Uint8Array(text.length)

  for (const { category, noLeadTrim, regexes } of rules) {
    if (spans.length >= maxMatches) break
    if (shouldStop?.()) return { spans, complete: stopEarly() }
    for (const regex of regexes) {
      if (spans.length >= maxMatches) break
      if (shouldStop?.()) return { spans, complete: stopEarly() }
      regex.lastIndex = 0
      let match: RegExpExecArray | null
      while ((match = regex.exec(text)) !== null) {
        if (spans.length >= maxMatches) break
        if (shouldStop?.()) return { spans, complete: stopEarly() }
        if (match[0].length === 0) {
          regex.lastIndex++
          continue
        }
        // Trim the consumed left word guard (group 1) — unless the rule
        // marks its group as part of the span (run anchor).
        const lead = noLeadTrim ? 0 : (match[1] ? match[1].length : 0)
        const start = match.index + lead
        const end = match.index + match[0].length
        if (end <= start) continue

        let isOverlapping = false
        for (let k = start; k < end; k++) {
          if (occupied[k]) {
            isOverlapping = true
            break
          }
        }
        if (isOverlapping) continue

        spans.push({ start, end, category })
        for (let k = start; k < end; k++) occupied[k] = 1
      }
    }
  }
  spans.sort((a, b) => a.start - b.start)
  // If the cap fired, the text wasn't fully scanned — report incomplete so
  // callers don't cache the truncated result as final.
  return { spans, complete: spans.length < maxMatches }
}

// --- Soft-wrap stitching -----------------------------------------------------
//
// The xterm buffer marks rows produced by terminal auto-wrap with
// `IBufferLine.isWrapped`, so a logical line the terminal folded onto several
// rows can be rebuilt: concatenate the rows' text, match rules over the whole
// logical line, then split the resulting spans back to row-local offsets.
// This makes constructs that span a wrap seam (quoted strings, URLs,
// interface names…) highlight as if the line were never folded, while hard
// newlines stay separate.

export interface StitchedRows {
  text: string
  /** Char index in `text` where each input row starts (same order/length). */
  starts: number[]
}

/** Join the texts of rows known to belong to one logical line (the caller
 * walks `isWrapped` to collect them). */
export function stitchLogicalText(texts: string[]): StitchedRows {
  const starts: number[] = []
  let offset = 0
  for (const text of texts) {
    starts.push(offset)
    offset += text.length
  }
  return { text: texts.join(''), starts }
}

/** Split spans over the full logical text into per-row, row-local spans.
 * A span crossing a wrap seam is clipped into one span per row it touches.
 * Returns one (possibly empty) span array per row, in row order. */
export function splitSpansToRows(
  spans: TextSpan[],
  starts: number[],
): TextSpan[][] {
  const perRow: TextSpan[][] = starts.map(() => [])
  if (spans.length === 0 || starts.length === 0) return perRow
  // Row r covers [starts[r], starts[r + 1]); the last row extends to
  // whatever the final span needs — its content is the tail of the text.
  let row = 0
  for (const span of spans) {
    while (row + 1 < starts.length && starts[row + 1] <= span.start) row++
    for (let r = row; r < starts.length && starts[r] < span.end; r++) {
      const rowEnd = r + 1 < starts.length ? starts[r + 1] : span.end
      const start = Math.max(span.start, starts[r])
      const end = Math.min(span.end, rowEnd)
      if (end > start) {
        perRow[r].push({ start: start - starts[r], end: end - starts[r], category: span.category })
      }
    }
  }
  return perRow
}
