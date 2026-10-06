import { describe, it, expect } from 'vitest'
import { HIGHLIGHT_RULES, matchTextSpans, stitchLogicalText, splitSpansToRows, type HighlightCategory, type TextSpan } from './highlightRules'

/** Match one line of plain text and flatten the spans to text+category. */
function spans(text: string): Array<{ text: string; category: HighlightCategory }> {
  return matchTextSpans(text, HIGHLIGHT_RULES)
    .spans
    .map((s) => ({ text: text.slice(s.start, s.end), category: s.category }))
}

/** The span (if any) whose text contains the given substring. */
function find(text: string, substr: string, category: HighlightCategory): boolean {
  return spans(text).some((s) => s.text.includes(substr) && s.category === category)
}

function noSpanOf(text: string, category: HighlightCategory): boolean {
  return !spans(text).some((s) => s.category === category)
}

describe('matchTextSpans — keyword rules', () => {
  it('highlights IP addresses as host', () => {
    expect(spans('ping 10.1.0.13\n')).toContainEqual({ text: '10.1.0.13', category: 'host' })
  })

  it('restricts the IP first octet to 1-254', () => {
    expect(noSpanOf('src 0.1.2.3\n', 'host')).toBe(true)
    expect(noSpanOf('src 255.1.2.3\n', 'host')).toBe(true)
    expect(find('src 254.1.2.3\n', '254.1.2.3', 'host')).toBe(true)
  })

  it('colors localhost, null and none as host', () => {
    expect(find('connecting to localhost\n', 'localhost', 'host')).toBe(true)
    expect(find('status: null\n', 'null', 'host')).toBe(true)
    expect(find('mode: none\n', 'none', 'host')).toBe(true)
  })

  it('colors IPv6 addresses as host (full and :: compressed forms)', () => {
    expect(find('addr 2001:db8:0:0:0:0:2:1 end\n', '2001:db8:0:0:0:0:2:1', 'host')).toBe(true)
    expect(find('inet6 fe80::1/64\n', 'fe80::1', 'host')).toBe(true)
    expect(find('listening on ::1\n', '::1', 'host')).toBe(true)
    expect(find('route 2001:db8::/32 via gw\n', '2001:db8::', 'host')).toBe(true)
  })

  it('does not highlight MAC addresses or :: scope tokens as IPv6', () => {
    expect(noSpanOf('mac aa:bb:cc:dd:ee:ff\n', 'host')).toBe(true)
    expect(noSpanOf('std::vector<int>\n', 'host')).toBe(true)
    expect(noSpanOf('at 12:34:56 sharp\n', 'host')).toBe(true)
  })

  it('does not misread MAC addresses as clock times', () => {
    // The all-digit clock pattern must stay out of any hex:hex:… run.
    expect(noSpanOf('ether 52:54:00:1d:87:9f\n', 'datetime')).toBe(true)
    expect(noSpanOf('mac 00:11:22:33:44:55\n', 'datetime')).toBe(true)
    // A real time next to a MAC still highlights.
    expect(find('at 12:34:56 mac 52:54:00:1d:87:9f\n', '12:34:56', 'datetime')).toBe(true)
  })

  it('colors error words as error', () => {
    expect(find('connect: connection refused\n', 'connection refused', 'error')).toBe(true)
    expect(find('ERROR: bad address\n', 'ERROR', 'error')).toBe(true)
    expect(find('ERROR: bad address\n', 'bad address', 'error')).toBe(true)
    expect(find('kernel panic: segmentation fault\n', 'segmentation fault', 'error')).toBe(true)
  })

  it('colors fatal/critical/exception/panic/abort as error', () => {
    expect(find('FATAL: unrecoverable state\n', 'FATAL', 'error')).toBe(true)
    expect(find('critical failure in module\n', 'critical', 'error')).toBe(true)
    expect(find('java.lang.Exception thrown\n', 'Exception', 'error')).toBe(true)
    expect(find('panic: runtime error\n', 'panic', 'error')).toBe(true)
    expect(find('transaction aborted\n', 'aborted', 'error')).toBe(true)
  })

  it('colors falsy output values (false/no/ko) as error when punctuation-guarded', () => {
    // NOTE: buffer rows never contain newlines (translateToString per row),
    // so the trailing-$ lookahead works on these inputs as it does live.
    expect(find('Result: no', 'no', 'error')).toBe(true)
    expect(find('=> false', 'false', 'error')).toBe(true)
    // prose must stay untouched
    expect(spans('nothing here')).toEqual([])
  })

  it('colors success words as success', () => {
    expect(find('session connected\n', 'connected', 'success')).toBe(true)
    expect(find('request accepted\n', 'accepted', 'success')).toBe(true)
    expect(find('deployed successfully\n', 'successfully', 'success')).toBe(true)
  })

  it('colors warning words as warning', () => {
    expect(find('WARNING: low disk\n', 'WARNING', 'warning')).toBe(true)
    expect(find('file not found\n', 'not found', 'warning')).toBe(true)
    expect(find('cannot open device\n', 'cannot', 'warning')).toBe(true)
  })

  it('colors bare warn/warned as warning', () => {
    expect(find('WARN: falling back to defaults\n', 'WARN', 'warning')).toBe(true)
    expect(find('user was warned about the risk\n', 'warned', 'warning')).toBe(true)
    expect(find('caution: legacy mode\n', 'caution', 'warning')).toBe(true)
  })

  it('colors info verbs as info', () => {
    expect(find('starting service…\n', 'starting', 'info')).toBe(true)
    expect(find('(ii) loading module\n', '(ii)', 'info')).toBe(true)
  })

  it('highlights URLs as url', () => {
    expect(find('see https://example.com/a?b=1 now\n', 'https://example.com', 'url')).toBe(true)
  })

  it('does not highlight arbitrary numbers', () => {
    expect(spans('port 8080 and 42\n')).toEqual([])
  })

  it('does not match keywords inside words', () => {
    expect(spans('terror attack\n')).toEqual([])
    expect(spans('warninglabel\n')).toEqual([])
    expect(spans('noteworthy\n')).toEqual([])
    expect(spans('exceptionally good\n')).toEqual([])
    expect(spans('fatality report\n')).toEqual([])
  })

  it('is case-insensitive', () => {
    expect(find('Connection Refused\n', 'Connection Refused', 'error')).toBe(true)
    expect(find('PING 10.0.0.1\n', '10.0.0.1', 'host')).toBe(true)
  })
})

describe('matchTextSpans — retained rules', () => {
  it('highlights a path containing the + special char', () => {
    const input = 'compiler /usr/local/gcc-11.5.0/bin/g++ -std=c++17\n'
    // The path anchor consumes the preceding whitespace (`(^|\s)` guard
    // group), so the space itself is NOT part of the span.
    expect(find(input, '/usr/local/gcc-11.5.0/bin/g++', 'path')).toBe(true)
    expect(spans(input).some((s) => s.text.startsWith(' '))).toBe(false)
  })

  it('highlights a path containing @ and ~', () => {
    const input = 'load /tmp/cache@1.tgz and ~/proj-2/app.exe now\n'
    expect(find(input, '/tmp/cache@1.tgz', 'path')).toBe(true)
    expect(find(input, '~/proj-2/app.exe', 'path')).toBe(true)
  })

  it('does not treat a bare an+b expression as a path', () => {
    expect(noSpanOf('compute an+b + c then d+e\n', 'path')).toBe(true)
  })

  it('highlights timestamps as datetime', () => {
    expect(find('at 12:34:56 done\n', '12:34:56', 'datetime')).toBe(true)
  })

  it('highlights quoted strings as string', () => {
    expect(find('msg "hello world" end\n', '"hello world"', 'string')).toBe(true)
  })

  it('highlights braces as brace', () => {
    expect(spans('hello {world}').some((s) => s.category === 'brace')).toBe(true)
  })
})

describe('matchTextSpans — network device config rules', () => {
  it('highlights Cisco interface names as ifname', () => {
    expect(find('interface FastEthernet0/1\n', 'FastEthernet0/1', 'ifname')).toBe(true)
    expect(find('interface GigabitEthernet0/0/1\n', 'GigabitEthernet0/0/1', 'ifname')).toBe(true)
    expect(find('interface TenGigabitEthernet1/0/1\n', 'TenGigabitEthernet1/0/1', 'ifname')).toBe(true)
    expect(find('interface Port-channel1\n', 'Port-channel1', 'ifname')).toBe(true)
  })

  it('highlights Huawei interface names as ifname', () => {
    expect(find('interface Vlanif204\n', 'Vlanif204', 'ifname')).toBe(true)
    expect(find('interface Eth-Trunk3\n', 'Eth-Trunk3', 'ifname')).toBe(true)
    expect(find('interface MEth0/0/1\n', 'MEth0/0/1', 'ifname')).toBe(true)
    expect(find('interface LoopBack0\n', 'LoopBack0', 'ifname')).toBe(true)
    expect(find('interface GE1/0/1\n', 'GE1/0/1', 'ifname')).toBe(true)
  })

  it('highlights ifnames without the interface keyword too', () => {
    expect(find('port trunk allow-pass vlan 10 17 135 on GigabitEthernet0/0/1\n', 'GigabitEthernet0/0/1', 'ifname')).toBe(true)
  })

  it('does not highlight lookalike words as ifname', () => {
    expect(noSpanOf('page1/2 of the report\n', 'ifname')).toBe(true)
    expect(noSpanOf('the ethernet cable is unplugged\n', 'ifname')).toBe(true)
    expect(noSpanOf('revisions 3vlan4 mixed\n', 'ifname')).toBe(true)
  })

  it('highlights device config keywords as keyword', () => {
    expect(find(' switchport access vlan 135\n', 'switchport access', 'keyword')).toBe(true)
    expect(find(' ip address 192.168.1.253 255.255.255.0\n', 'ip address', 'keyword')).toBe(true)
    expect(find(' port link-type trunk\n', 'port link-type', 'keyword')).toBe(true)
    expect(find(' port trunk allow-pass vlan 10 17 135\n', 'port trunk', 'keyword')).toBe(true)
    expect(find(' dhcp server dns-list 192.168.1.12\n', 'dhcp server', 'keyword')).toBe(true)
    expect(find(' dhcp select interface\n', 'dhcp select', 'keyword')).toBe(true)
    expect(find(' mode lacp\n', 'mode lacp', 'keyword')).toBe(true)
    expect(find(' vlan batch 10 20\n', 'vlan batch', 'keyword')).toBe(true)
    expect(find(' ip route-static 0.0.0.0 0 192.168.1.1\n', 'ip route-static', 'keyword')).toBe(true)
  })

  it('highlights the interface keyword only before an interface name', () => {
    expect(find('interface GigabitEthernet0/0/1\n', 'interface', 'keyword')).toBe(true)
    expect(find('interface Vlanif204\n', 'interface', 'keyword')).toBe(true)
    expect(find('interface Vlan10\n', 'interface', 'keyword')).toBe(true)
    expect(find('interface LoopBack0\n', 'interface', 'keyword')).toBe(true)
    // Prose and non-device contexts stay untouched.
    expect(noSpanOf('the network interface card is fine\n', 'keyword')).toBe(true)
    expect(noSpanOf('no such interface\n', 'keyword')).toBe(true)
  })

  it('does not highlight keyword lookalikes', () => {
    expect(noSpanOf('switchports available\n', 'keyword')).toBe(true)
    expect(noSpanOf('trip addresses\n', 'keyword')).toBe(true)
  })

  it('keeps IP highlighting alongside keywords', () => {
    const line = ' ip address 192.168.1.253 255.255.255.0\n'
    expect(find(line, 'ip address', 'keyword')).toBe(true)
    expect(find(line, '192.168.1.253', 'host')).toBe(true)
    // 255.x netmasks stay uncolored: the host rule intentionally restricts
    // the first octet to 1-254 (see the keyword-rule suite above).
  })
})

describe('matchTextSpans — span mechanics', () => {
  it('matches a run of consecutive identical brace symbols once', () => {
    const runs = spans('a **** b ===== c >>> d')
      .filter((s) => s.category === 'brace')
    expect(runs).toEqual([
      { text: '****', category: 'brace' },
      { text: '=====', category: 'brace' },
      { text: '>>>', category: 'brace' },
    ])
  })

  it('never overlaps spans: the first matching rule wins', () => {
    // The URL rule consumes the whole link; path/host/brace must not
    // double-cover parts of it.
    const input = 'see https://example.com/a?b=1 now'
    const raw = matchTextSpans(input, HIGHLIGHT_RULES).spans
    for (let i = 0; i < raw.length; i++) {
      for (let j = i + 1; j < raw.length; j++) {
        expect(raw[j].start >= raw[i].end || raw[i].start >= raw[j].end).toBe(true)
      }
    }
  })

  it('returns spans sorted by position regardless of rule order', () => {
    // path matches at a later column than datetime; datetime's rule runs
    // after path's, so unsorted output would interleave.
    const input = '2026-09-16 20:50:36,266 p=156 u=root | included: /opt/extra/init-base/roles/network-bond.yml\n'
    const raw = matchTextSpans(input, HIGHLIGHT_RULES).spans
    for (let i = 1; i < raw.length; i++) {
      expect(raw[i].start).toBeGreaterThanOrEqual(raw[i - 1].end)
    }
    // The timestamp (first columns) and the `=` signs must all be matched.
    expect(find(input, '2026-09-16 20:50:36,266', 'datetime')).toBe(true)
    expect(spans(input).some((s) => s.text === '=' && s.category === 'brace')).toBe(true)
    expect(find(input, '/opt/extra/init-base/roles/network-bond.yml', 'path')).toBe(true)
  })

  it('is stable when given an empty string', () => {
    expect(matchTextSpans('').spans).toEqual([])
  })

  it('respects the maxMatches cap and reports incompleteness', () => {
    const input = '{a} {b} {c} {d} {e} {f} {g} {h}'
    const result = matchTextSpans(input, HIGHLIGHT_RULES, { maxMatches: 3 })
    expect(result.spans.length).toBe(3)
    expect(result.complete).toBe(false)
  })
})

describe('stitchLogicalText / splitSpansToRows — soft-wrap stitching', () => {
  it('joins wrapped rows and records each row start offset', () => {
    const { text, starts } = stitchLogicalText(['abc', 'def', 'gh'])
    expect(text).toBe('abcdefgh')
    expect(starts).toEqual([0, 3, 6])
  })

  it('handles empty rows and a single row', () => {
    expect(stitchLogicalText(['']).text).toBe('')
    const single = stitchLogicalText(['abc'])
    expect(single).toEqual({ text: 'abc', starts: [0] })
  })

  it('clips a span crossing the row seam into two row-local spans', () => {
    // 'bond-business' straddles the seam after 'bond-bus' (row width 8)
    const spans: TextSpan[] = [{ start: 4, end: 17, category: 'string' }]
    const perRow = splitSpansToRows(spans, [0, 13])
    expect(perRow[0]).toEqual([{ start: 4, end: 13, category: 'string' }])
    expect(perRow[1]).toEqual([{ start: 0, end: 4, category: 'string' }])
  })

  it('keeps a span inside one row untouched and distributes many spans', () => {
    const spans: TextSpan[] = [
      { start: 0, end: 2, category: 'datetime' },
      { start: 3, end: 5, category: 'host' },
      { start: 15, end: 18, category: 'string' },
    ]
    const perRow = splitSpansToRows(spans, [0, 10, 20])
    expect(perRow[0]).toEqual([
      { start: 0, end: 2, category: 'datetime' },
      { start: 3, end: 5, category: 'host' },
    ])
    expect(perRow[1]).toEqual([{ start: 5, end: 8, category: 'string' }])
    expect(perRow[2]).toEqual([])
  })

  it('splits a span reaching into the shorter last row', () => {
    const spans: TextSpan[] = [{ start: 8, end: 15, category: 'string' }]
    const perRow = splitSpansToRows(spans, [0, 10])
    expect(perRow[0]).toEqual([{ start: 8, end: 10, category: 'string' }])
    expect(perRow[1]).toEqual([{ start: 0, end: 5, category: 'string' }])
  })

  it('returns empty arrays for no spans', () => {
    expect(splitSpansToRows([], [0, 5])).toEqual([[], []])
  })
})

describe('matchTextSpans — string priority', () => {
  it('quoted strings win over every later rule (host, url, datetime, brace)', () => {
    // With the string rule first, a quoted IP/datetime/URL is one string
    // span; later rules must not carve matches out of it.
    const cases = [
      ['addr "192.168.10.3" ok', '192.168.10.3'],
      ['at 2026-05-18 09:10:36 done', '2026-05-18 09:10:36'],
      ['see https://example.com/a?b=c end', 'https://example.com/a?b=c'],
    ]
    for (const [input] of cases) {
      const result = matchTextSpans(`'${input}'`)
      expect(result.spans.length).toBe(1)
      expect(result.spans[0].category).toBe('string')
    }
  })
})

describe('matchTextSpans — quote pairing across empty strings', () => {
  it('keeps pairing aligned when an empty string appears before a longer one', () => {
    // Regression: `{2,}` skipped `""`, but its two quotes stayed in the scan
    // and the next match stole the closing one — every pair after it shifted,
    // and the long string below (holding an IP) never matched.
    const input = '"stderr": "", "stdout": "VIP: 100.126.255.250"'
    const result = matchTextSpans(input)
    const bigStart = input.indexOf('"VIP')
    const big = result.spans.find((s) => s.category === 'string' && s.start === bigStart)
    expect(big).toBeDefined()
    expect(input.slice(big!.start, big!.end)).toBe('"VIP: 100.126.255.250"')
    // and the IP is string, not host
    const ipAt = input.indexOf('100.126.255.250')
    expect(result.spans.some((s) => s.category === 'string' && s.start <= ipAt && ipAt < s.end)).toBe(true)
  })

  it('matches an empty string as a 2-char span', () => {
    const result = matchTextSpans('a "" b')
    expect(result.spans.some((s) => s.category === 'string' && s.start === 2 && s.end === 4)).toBe(true)
  })
})
