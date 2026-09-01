// Reset only underline (24) and foreground color (39), leaving background
// color intact. Using \x1b[0m (full reset) would clear vim's visual selection
// background and other SGR attributes set by terminal applications.
const ANSI_RESET = '\x1b[24;39m'
// Matches a single SGR color sequence (`CSI …m`). Any other CSI sequence
// (cursor movement, erase, private modes…) does not change the foreground.
const SGR_SEQ = /^\x1b\[[\d;]*m$/
// Match ANSI escape sequences: CSI (ESC [ ... letter) and OSC (ESC ] ... BEL/ST)
const ANSI_RE = /(\x1b\[[\x20-\x3F]*[\x40-\x7E]|\x1b[\]PX^_][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[\x20-\x2F][\x30-\x7E]|\x1b[\x30-\x7E])/g

// Split text into segments: alternating [plain, CSI, plain, CSI, ...]
function segmentText(text: string): { text: string; isCSI: boolean }[] {
  const segments: { text: string; isCSI: boolean }[] = []
  let lastEnd = 0
  ANSI_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = ANSI_RE.exec(text)) !== null) {
    if (m.index > lastEnd) {
      segments.push({ text: text.slice(lastEnd, m.index), isCSI: false })
    }
    segments.push({ text: m[0], isCSI: true })
    lastEnd = m.index + m[0].length
  }
  if (lastEnd < text.length) {
    segments.push({ text: text.slice(lastEnd), isCSI: false })
  }
  if (segments.length === 0) {
    segments.push({ text, isCSI: false })
  }
  return segments
}

// ── Color palette ──
// ANSI SGR codes (30-37 / 90-97) so highlight colors follow the terminal
// theme's palette.
const C = {
  url:      '\x1b[4;34m',
  host:     '\x1b[35m',
  path:     '\x1b[35m',
  datetime: '\x1b[94m',
  string:   '\x1b[33m',
  success:  '\x1b[32m',
  error:    '\x1b[31m',
  warning:  '\x1b[33m',
  info:     '\x1b[36m',
  brace:    '\x1b[95m',
} as const

// Group 1, when present, is a consumed left word guard that is NOT part of
// the highlighted span (highlightPlainSegment trims m[1]). Right guards are
// zero-width lookaheads. No lookbehind anywhere — unsupported by the
// JavaScriptCore in macOS ≤12.3 WebView, where this module fails to parse.
const PATTERNS: { sgr: string; regexes: RegExp[] }[] = [
  { sgr: C.url,     regexes: [
    /https?:\/\/[A-Za-z0-9_.&?=%~#{}()@+-]+(?::?[A-Za-z0-9_./&?=%~#{}()@+-]+)?/gi,
  ]},
  { sgr: C.host,    regexes: [
    // IPv4 (first octet 1-254) and IPv6 (full and ::-compressed forms)
    /(^|[^0-9a-z_&-])(localhost|(?:1[0-9][0-9]|2[0-4][0-9]|25[0-4]|[1-9][0-9]|[1-9])\.\d+\.\d+\.\d+|null|none)(?![0-9a-z_-])/gi,
    /(^|[^0-9a-z_&-])((?:[a-f0-9]{1,4}:){7}[a-f0-9]{1,4}|(?:[a-f0-9]{1,4}:){1,7}:|(?:[a-f0-9]{1,4}:){1,6}:[a-f0-9]{1,4}|(?:[a-f0-9]{1,4}:){1,5}(?::[a-f0-9]{1,4}){1,2}|(?:[a-f0-9]{1,4}:){1,4}(?::[a-f0-9]{1,4}){1,3}|(?:[a-f0-9]{1,4}:){1,3}(?::[a-f0-9]{1,4}){1,4}|(?:[a-f0-9]{1,4}:){1,2}(?::[a-f0-9]{1,4}){1,5}|[a-f0-9]{1,4}:(?::[a-f0-9]{1,4}){1,6}|:(?::[a-f0-9]{1,4}){1,7})(?![0-9a-f:])/gi,
  ]},
  { sgr: C.error,   regexes: [
    // "<adjective> <noun>" phrases: bad address, invalid argument, …
    /(^|[^a-z_&-])((?:bad|wrong|incorrect|improper|invalid|unsupported)(?: file| memory)? (?:descriptor|alloc(?:ation)?|addr(?:ess)?|owner(?:ship)?|arg(?:ument)?|param(?:eter)?|setting|length|filename))(?![a-z_-])/gi,
    // denied, failed, segfault, no X found, …
    /(^|[^a-z_&-])((?:operation |connection |authentication |access |permission )?(?:denied|disallowed|not allowed|refused|problem|failed|failure|not permitted)|not properly|improperly|no [a-z]+(?: [a-z]+)? found|invalid|unsupported|not supported|seg(?:mentation )?fault|corrupt(?:ion|ed)?|overflow|underrun|not ok|unimplemented|unsuccessfull?|not implemented|permerrors?|errors?|crash(?:ed)?|core dump|\(ee\)|\(ni\))(?![a-z_-])/gi,
    // falsy output values ("=> no", "status: false")
    /([=>"':.,;({\[] *)(?:false|no|ko)(?=[\]=>"':.,;)} ]|$)/gi,
  ]},
  { sgr: C.success, regexes: [
    /(^|[^a-z_&-])(accepted|allowed|enabled|connected|successfully|successful|succeeded|success)(?![a-z_-])/gi,
  ]},
  { sgr: C.warning, regexes: [
    /(^|[^a-z_&-])(\[-w[a-z-]+\]|caught signal [0-9]+|cannot|not responding|(?:connection (?:to (?:remote host|[a-z0-9.]+) )?)?(?:closed|terminated|stopped)|exited|no more [a-z]+ available|unexpected|(?:command |binary |file )?not found|o{2,}ps|out of (?:space|memory)|low (?:memory|disk)|unknown|disabled|disconnect(?:ed|ion)?|deprecated|refused|warnings?|\(ww\)|\(\?\?\)|could not|unable to)(?![a-z_-])/gi,
  ]},
  { sgr: C.info,    regexes: [
    /(^|[^a-z_&-])(last (?:failed )?login:|launching|checking|loading|creating|building|important|booting|starting|informational|informations?|info|notice|note|\(ii\)|\(\!\!\))(?![a-z_-])/gi,
  ]},
  // Character class includes `+`, `~`, `@` so paths like
  // `/usr/local/gcc-11.5.0/bin/g++` are recognised whole.
  { sgr: C.path,    regexes: [/(^|\s)(?:\/|~\/)[\w.+~@/-]+(?=[\s:;"')\]}]|$)/g] },
  { sgr: C.datetime, regexes: [
    /\b\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(?::\d{2})?(?:[.,]\d+)?Z?\b/g,
    /\b(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)\s+(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4}\b/g,
    /\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\b/g,
    /\b\d{2}:\d{2}:\d{2}\b/g,
  ]},
  { sgr: C.string,  regexes: [/"(?:[^"\\]|\\.){2,}"|'(?:[^'\\]|\\.){2,}'/g] },
  { sgr: C.brace,   regexes: [/[{}()\[\]|*=<>]/g] },
]

// Report how an SGR sequence affects the foreground color:
//  - 'set'  a non-default foreground color is now active (30-37, 90-97, 38…)
//  - 'clear' foreground reset to default (0, 39)
//  - 'unchanged' leaves the foreground untouched (bold, underline, 48… bg…)
// Sequences are evaluated in order, so e.g. `0;34` is 'set' and `34;0` 'clear'.
function sgrFgEffect(seq: string): 'set' | 'clear' | 'unchanged' {
  const body = seq.slice(2, -1)  // strip \x1b[ and trailing m
  const params = body === '' ? [0] : body.split(';').map((s) => (s === '' ? 0 : parseInt(s, 10)))
  let effect: 'set' | 'clear' | 'unchanged' = 'unchanged'
  for (let i = 0; i < params.length; i++) {
    const p = params[i]
    if (p === 0 || p === 39) {
      effect = 'clear'
    } else if ((p >= 30 && p <= 37) || (p >= 90 && p <= 97)) {
      effect = 'set'
    } else if (p === 38) {
      // Extended foreground: 38;5;N or 38;2;R;G;B
      const kind = params[i + 1]
      if (kind === 5) {
        effect = 'set'
        i += 2
      } else if (kind === 2) {
        effect = 'set'
        i += 4
      }
    }
  }
  return effect
}

type HighlightSegmentOpts = { skipBrace?: boolean }

function highlightPlainSegment(text: string, opts: HighlightSegmentOpts = {}): string {
  type MatchEntry = { start: number; end: number; sgr: string }
  const allMatches: MatchEntry[] = []
  const patterns = opts.skipBrace ? PATTERNS.filter((p) => p.sgr !== C.brace) : PATTERNS
  for (const { sgr, regexes } of patterns) {
    for (const regex of regexes) {
      regex.lastIndex = 0
      let m: RegExpExecArray | null
      while ((m = regex.exec(text)) !== null) {
        const lead = m[1] ? m[1].length : 0  // skip the consumed left guard
        allMatches.push({ start: m.index + lead, end: m.index + m[0].length, sgr })
        if (allMatches.length > 200) break
      }
      if (allMatches.length > 200) break
    }
    if (allMatches.length > 200) break
  }
  if (allMatches.length > 200) {
    return text  // pass through unchanged
  }
  allMatches.sort((a, b) => a.start - b.start || b.end - a.end)
  const filtered: MatchEntry[] = []
  for (const match of allMatches) {
    const last = filtered[filtered.length - 1]
    if (!last || match.start >= last.end) {
      filtered.push(match)
    }
  }
  let highlighted = text
  for (let i = filtered.length - 1; i >= 0; i--) {
    const { start, end, sgr } = filtered[i]
    highlighted = highlighted.slice(0, start) + sgr + highlighted.slice(start, end) + ANSI_RESET + highlighted.slice(end)
  }
  return highlighted
}

function highlightPlainText(text: string, opts: HighlightSegmentOpts = {}): string {
  const segments = segmentText(text)
  let result = ''
  // Track whether the upstream application already set a non-default
  // foreground color (e.g. `ls` coloring a directory name blue); such spans
  // must not be re-colored. Only default-foreground text gets styled by us.
  let colored = false
  for (const seg of segments) {
    if (seg.isCSI) {
      if (SGR_SEQ.test(seg.text)) {
        const effect = sgrFgEffect(seg.text)
        if (effect !== 'unchanged') colored = effect === 'set'
      }
      result += seg.text
    } else if (!colored) {
      result += highlightPlainSegment(seg.text, opts)
    } else {
      result += seg.text
    }
  }
  return result
}

// Line contains only plain text and SGR (`\x1b[…m`) escapes — i.e. upstream
// merely coloured parts of the line. Cursor movement / erase / OSC / private
// modes indicate a TUI app (k9s, vim, htop…) drawing a screen; those lines
// must stay untouched.
const SGR_ONLY_LINE = /^(?:[^\x1b]|\x1b\[[\d;]*m)*$/
const FENCE_OPEN = /^\s{0,3}(`{3,}|~{3,})/
const INDENTED_CODE_LINE = /^ {4,}\S/

export function highlight(text: string): string {
  // Process line by line to avoid cross-line regex matches.
  const lines = text.split(/(\r?\n)/)
  let result = ''
  let fenceChar: '`' | '~' | null = null
  for (const line of lines) {
    if (line === '\r\n' || line === '\n' || line === '\r') {
      result += line
    } else if (line) {
      // For SGR-only lines, let highlightPlainText run — it splits on CSI
      // boundaries and only touches the plain segments, so already-coloured
      // spans pass through unchanged while the uncoloured remainder still
      // gets highlighted. Same for plain text lines.
      if (line.indexOf('\x1b') !== -1 && !SGR_ONLY_LINE.test(line)) {
        result += line
      } else {
        const m = FENCE_OPEN.exec(line)
        if (fenceChar) {
          // Inside a fenced block — pass through until matching close fence.
          if (m && m[1][0] === fenceChar) fenceChar = null
          result += line
        } else if (m) {
          // Opening fence (``` or ~~~) — re-coloring braces / brackets
          // inside fenced code injects SGR resets that some TUI apps
          // misinterpret and produce overlapping glyphs.
          fenceChar = m[1][0] as '`' | '~'
          result += line
        } else if (INDENTED_CODE_LINE.test(line)) {
          // Brace/bracket colour injects SGR noise that some TUI apps
          // misinterpret, so keep skipping it here — but colour the
          // remaining tokens normally.
          result += highlightPlainText(line, { skipBrace: true })
        } else {
          result += highlightPlainText(line)
        }
      }
    }
  }
  return result
}
