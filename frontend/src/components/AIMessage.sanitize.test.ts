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
    const safe = '<a href="https://x/a?b=1" target="_blank">x</a>'
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
