// Regression tests for FE-01 (XSS via markdown-produced HTML in the
// AIMessage.vue v-html binding) and the FE-01 follow-up: attribute breakout
// via slash-separated on* handlers (<a href="x"/onclick="...">), which the
// whitespace-only strip missed. The real helpers are imported directly (an
// earlier revision mirrored the implementation, which drifted silently).

import { describe, expect, it } from 'vitest'
import { sanitizeRenderedHtml, escapeHtml, renderMarkdownHtml } from '../utils/markdown'

describe('sanitizeRenderedHtml (FE-01 XSS)', () => {
  it('strips javascript: URLs from link href', () => {
    const out = sanitizeRenderedHtml(
      '<a href="javascript:fetch(\'http://x\')" target="_blank">click</a>',
    )
    expect(out).not.toMatch(/javascript:/i)
    expect(out).not.toMatch(/fetch\(/i)
    expect(out).toMatch(/target="_blank"/) // other attrs preserved
    expect(out).toMatch(/>click</)
  })

  it('strips attribute-quote breakout in image src', () => {
    const out = sanitizeRenderedHtml('<img src="x" onerror="alert(1)" alt="alt">')
    expect(out).not.toMatch(/onerror/i)
    expect(out).not.toMatch(/alert\(/i)
    expect(out).toMatch(/<img/) // tag still present
  })

  it('strips slash-separated on* handlers with quoted values', () => {
    const out = sanitizeRenderedHtml('<a href="x"/onclick="alert(1)">x</a>')
    expect(out).not.toMatch(/onclick/i)
    expect(out).not.toMatch(/alert\(/i)
    expect(out).toMatch(/<a/) // tag still present
  })

  it('keeps slash URLs containing on…= segments intact (no false positive)', () => {
    const url = '<a href="https://example.com/online=1">x</a>'
    expect(sanitizeRenderedHtml(url)).toBe(url)
  })

  it('strips <script> tags and their content', () => {
    const out = sanitizeRenderedHtml('before<script>alert(1)</script>after')
    expect(out).not.toMatch(/<script/i)
    expect(out).not.toMatch(/alert\(/)
    expect(out).toMatch(/before/)
    expect(out).toMatch(/after/)
  })

  it('strips iframe / object / embed entirely', () => {
    const out = sanitizeRenderedHtml(
      '<iframe src="https://evil"></iframe><object data="x"></object><embed src="y">',
    )
    expect(out).not.toMatch(/iframe/i)
    expect(out).not.toMatch(/object/i)
    expect(out).not.toMatch(/embed/i)
  })

  it('preserves safe content unchanged', () => {
    const safe = '<p>hello <strong>world</strong></p>'
    expect(sanitizeRenderedHtml(safe)).toBe(safe)
  })
})

describe('markdown pipeline attribute-breakout hardening (FE-01 follow-up)', () => {
  it('escapeHtml escapes double quotes', () => {
    const out = escapeHtml('[x](x"/onerror="alert(1))')
    expect(out).not.toMatch(/"/)
    expect(out).toContain('&quot;')
  })

  it('renderMarkdownHtml output has no raw quotes from model content', () => {
    // With quotes escaped at the source, the link-syntax payload
    // [x](x"/onclick="alert(1)) lands inside the quoted href value with its
    // quotes entity-encoded — inert URL text that can never split into a
    // separate attribute.
    const out = sanitizeRenderedHtml(renderMarkdownHtml('[x](x"/onclick="alert(1))'))
    expect(out).toContain('href="x&quot;/onclick=&quot;alert(1"')
    expect(out).not.toMatch(/[\s/]"?onclick="\s*alert/)
  })

  it('renderMarkdownHtml keeps ordinary links working', () => {
    const out = renderMarkdownHtml('[docs](https://example.com/a?b=1)')
    expect(out).toContain('href="https://example.com/a?b=1"')
  })
})

describe('sanitizeRenderedHtml (whitespace-obfuscated URL schemes)', () => {
  // HTML URL parsing strips tab/newline/formfeed inside the href value, so
  // `java\tscript:` reaches the browser as `javascript:`. The sanitizer must
  // remove the whole attribute, not just the scheme literal.

  it('strips href whose scheme is tab-obfuscated', () => {
    const out = sanitizeRenderedHtml('<a href="java\tscript:alert(1)">x</a>')
    expect(out).not.toMatch(/href/i)
    expect(out).not.toContain('java\tscript')
    expect(out).toContain('>x</a>')
  })

  it('strips href whose scheme is newline-obfuscated', () => {
    const out = sanitizeRenderedHtml('<a href="java\nscript:alert(1)">x</a>')
    expect(out).not.toMatch(/href/i)
    expect(out).not.toContain('java\nscript')
  })

  it('strips single-quoted href whose scheme is newline-obfuscated', () => {
    const out = sanitizeRenderedHtml("<a href='java\nscript:alert(1)'>x</a>")
    expect(out).not.toMatch(/href/i)
    expect(out).not.toContain('java\nscript')
  })

  it('strips src whose scheme is newline-obfuscated data:', () => {
    const out = sanitizeRenderedHtml('<img src="da\nta:text/html;base64,PHNjcmlwdD4=">')
    expect(out).not.toMatch(/src/i)
    expect(out).not.toContain('da\nta:')
  })

  it('strips scheme regardless of letter case', () => {
    const out = sanitizeRenderedHtml('<a href="JaVa\tScRiPt:alert(1)">x</a>')
    expect(out).not.toMatch(/href/i)
    expect(out).not.toContain('JaVa\tScRiPt')
  })

  it('leaves safe quoted hrefs byte-identical (positive control)', () => {
    // Canonical renderer output now includes rel="noopener" on _blank
    // anchors (F4 enforcement below), so the safe control carries it too.
    const safe = '<a href="https://x/a?b=1" target="_blank" rel="noopener">x</a>'
    expect(sanitizeRenderedHtml(safe)).toBe(safe)
    const path = '<a href="/online=1">x</a>'
    expect(sanitizeRenderedHtml(path)).toBe(path)
  })

  it('still strips unquoted javascript: href', () => {
    const out = sanitizeRenderedHtml('<a href=javascript:alert(1)>x</a>')
    expect(out).not.toMatch(/href/i)
    expect(out).not.toMatch(/javascript:/i)
  })
})

describe('sanitizeRenderedHtml (F4 auto-link rel enforcement)', () => {
  // The auto-link path (autoLinkUrls in AIMessage.vue) emits raw
  // <a ... target="_blank"> anchors without rel, unlike regular markdown
  // links which carry rel="noopener" at render time. The sanitizer is the
  // shared choke point every renderer output passes through, so it
  // enforces the attribute.

  it('adds rel="noopener" to auto-linked anchors missing it', () => {
    // Exact shape autoLinkUrls produces for a bare URL in message text.
    const out = sanitizeRenderedHtml(
      '<a href="https://example.com/x" target="_blank">https://example.com/x</a>',
    )
    expect(out).toContain('target="_blank" rel="noopener"')
    expect(out.match(/rel=/g)).toHaveLength(1)
  })

  it('does not double up rel on anchors that already carry one', () => {
    // Regular markdown-link output — must stay byte-identical so the
    // enforcement cannot churn renderer output it already agrees with.
    const md = '<a href="https://example.com" target="_blank" rel="noopener">docs</a>'
    expect(sanitizeRenderedHtml(md)).toBe(md)
  })
})

describe('sanitizeRenderedHtml (F6 SVG/MathML namespace vectors)', () => {
  // Namespace-confusion / mXSS probes: the browser's HTML parser reparents
  // svg/math foreign content differently than a naive reader expects, so a
  // payload hidden in one namespace can mutate into live HTML. The
  // sanitizer already drops <svg>/<math>/<style> subtrees wholesale; these
  // pin that behavior so a refactor of the strip list cannot regress it.
  // Both closed forms (subtree removable) and unclosed forms (only the
  // opening tag is droppable) are pinned — the unclosed residue is where a
  // regression would hide.

  it('drops svg subtree containing a javascript: anchor', () => {
    const out = sanitizeRenderedHtml('<svg><a href="javascript:alert(1)">click</a></svg>')
    expect(out).toBe('')
  })

  it('neutralizes javascript: in svg href/xlink:href', () => {
    const out = sanitizeRenderedHtml(
      '<svg><image href="javascript:alert(1)"/><image xlink:href="javascript:alert(1)"/></svg>',
    )
    expect(out).toBe('')
  })

  it('neutralizes javascript: xlink:href when the svg tag leaks open', () => {
    // Unclosed <svg> can only drop its opening tag; the inner anchor must
    // still lose its href before anything reaches v-html.
    const out = sanitizeRenderedHtml('<svg><a xlink:href="javascript:alert(1)">x</a>')
    expect(out).not.toMatch(/javascript:/i)
    expect(out).not.toMatch(/href/i)
    expect(out).not.toMatch(/<svg/i)
  })

  it('drops closed math/mtext/table/mglyph/style mXSS probe entirely', () => {
    const out = sanitizeRenderedHtml(
      '<math><mtext><table><mglyph><style><!--</style><img title="--&gt;&lt;img src=1 onerror=alert(1)&gt;"></math>',
    )
    expect(out).toBe('')
  })

  it('strips math mXSS probe residue down to inert markup when unclosed', () => {
    const out = sanitizeRenderedHtml(
      '<math><mtext><table><mglyph><style><!--</style><img title="--&gt;&lt;img src=1 onerror=alert(1)&gt;">',
    )
    expect(out).not.toMatch(/<math/i)
    expect(out).not.toMatch(/<style/i)
    expect(out).not.toMatch(/onerror/i)
    expect(out).not.toMatch(/alert\(/)
  })

  it('drops closed svg/desc/p/style nested-content mXSS probe entirely', () => {
    const out = sanitizeRenderedHtml(
      '<svg><desc><p><style><!--</style><img title="--&gt;&lt;img src=1 onerror=alert(1)&gt;"></p></desc></svg>',
    )
    expect(out).toBe('')
  })

  it('strips svg/desc mXSS probe residue down to inert markup when unclosed', () => {
    const out = sanitizeRenderedHtml(
      '<svg><desc><p><style><!--</style><img title="--&gt;&lt;img src=1 onerror=alert(1)&gt;">',
    )
    expect(out).not.toMatch(/<svg/i)
    expect(out).not.toMatch(/<style/i)
    expect(out).not.toMatch(/onerror/i)
    expect(out).not.toMatch(/alert\(/)
  })
})
