import { describe, it, expect } from 'vitest'
import { highlight } from './useHighlight'

// SGR sequences inserted by highlight() — we only assert presence/absence,
// not exact bytes (the colour palette is intentionally allowed to evolve).

describe('highlight() — keyword rules', () => {
  it('highlights IP addresses in magenta', () => {
    const out = highlight('ping 10.1.0.13\n')
    expect(out).toContain('\x1b[35m10.1.0.13\x1b[24;39m')
  })

  it('restricts the IP first octet to 1-254', () => {
    expect(highlight('src 0.1.2.3\n')).not.toContain('\x1b[35m')
    expect(highlight('src 255.1.2.3\n')).not.toContain('\x1b[35m')
    expect(highlight('src 254.1.2.3\n')).toContain('\x1b[35m254.1.2.3')
  })

  it('colors localhost, null and none magenta like IPs', () => {
    expect(highlight('connecting to localhost\n')).toContain('\x1b[35mlocalhost\x1b[24;39m')
    expect(highlight('status: null\n')).toContain('\x1b[35mnull\x1b[24;39m')
    expect(highlight('mode: none\n')).toContain('\x1b[35mnone\x1b[24;39m')
  })

  it('colors IPv6 addresses magenta (full and :: compressed forms)', () => {
    expect(highlight('addr 2001:db8:0:0:0:0:2:1 end\n')).toContain('\x1b[35m2001:db8:0:0:0:0:2:1\x1b[24;39m')
    expect(highlight('inet6 fe80::1/64\n')).toContain('\x1b[35mfe80::1\x1b[24;39m')
    expect(highlight('listening on ::1\n')).toContain('\x1b[35m::1\x1b[24;39m')
    expect(highlight('http://[::1]:8080/\n')).toContain('\x1b[35m::1\x1b[24;39m')
    expect(highlight('route 2001:db8::/32 via gw\n')).toContain('\x1b[35m2001:db8::\x1b[24;39m')
  })

  it('does not highlight MAC addresses or :: scope tokens as IPv6', () => {
    expect(highlight('mac aa:bb:cc:dd:ee:ff\n')).not.toContain('\x1b[35m')
    expect(highlight('std::vector<int>\n')).not.toContain('\x1b[35m')
    expect(highlight('at 12:34:56 sharp\n')).not.toContain('\x1b[35m')
  })

  it('colors error words red', () => {
    expect(highlight('connect: connection refused\n')).toContain('\x1b[31mconnection refused\x1b[24;39m')
    expect(highlight('ERROR: bad address\n')).toContain('\x1b[31mERROR\x1b[24;39m')
    expect(highlight('ERROR: bad address\n')).toContain('\x1b[31mbad address\x1b[24;39m')
    expect(highlight('kernel panic: segmentation fault\n')).toContain('\x1b[31msegmentation fault\x1b[24;39m')
  })

  it('colors falsy output values (false/no/ko) red when punctuation-guarded', () => {
    expect(highlight('Result: no\n')).toContain('\x1b[31mno\x1b[24;39m')
    expect(highlight('=> false\n')).toContain('\x1b[31mfalse\x1b[24;39m')
    // prose must stay untouched
    expect(highlight('nothing here\n')).toBe('nothing here\n')
  })

  it('colors success words green', () => {
    expect(highlight('session connected\n')).toContain('\x1b[32mconnected\x1b[24;39m')
    expect(highlight('request accepted\n')).toContain('\x1b[32maccepted\x1b[24;39m')
    expect(highlight('deployed successfully\n')).toContain('\x1b[32msuccessfully\x1b[24;39m')
  })

  it('colors warning words yellow', () => {
    expect(highlight('WARNING: low disk\n')).toContain('\x1b[33mWARNING\x1b[24;39m')
    expect(highlight('file not found\n')).toContain('\x1b[33mfile not found\x1b[24;39m')
    expect(highlight('cannot open device\n')).toContain('\x1b[33mcannot\x1b[24;39m')
  })

  it('colors info verbs cyan', () => {
    expect(highlight('starting service…\n')).toContain('\x1b[36mstarting\x1b[24;39m')
    expect(highlight('(ii) loading module\n')).toContain('\x1b[36m(ii)\x1b[24;39m')
  })

  it('underlines URLs in blue', () => {
    const out = highlight('see https://example.com/a?b=1 now\n')
    expect(out).toContain('\x1b[4;34mhttps://example.com/a?b=1\x1b[24;39m')
  })

  it('no longer highlights arbitrary numbers', () => {
    expect(highlight('port 8080 and 42\n')).toBe('port 8080 and 42\n')
  })

  it('does not match keywords inside words', () => {
    expect(highlight('terror attack\n')).toBe('terror attack\n')
    expect(highlight('warninglabel\n')).toBe('warninglabel\n')
    expect(highlight('noteworthy\n')).toBe('noteworthy\n')
  })

  it('is case-insensitive', () => {
    expect(highlight('Connection Refused\n')).toContain('\x1b[31mConnection Refused\x1b[24;39m')
    expect(highlight('PING 10.0.0.1\n')).toContain('\x1b[35m10.0.0.1')
  })
})

describe('highlight() — retained rules', () => {
  it('highlights a path containing the + special char', () => {
    const input = 'compiler /usr/local/gcc-11.5.0/bin/g++ -std=c++17\n'
    const out = highlight(input)
    // The path anchor consumes the preceding whitespace (`(^|\s)` guard
    // group), so the space itself is NOT part of the coloured span.
    expect(out).toContain('\x1b[35m/usr/local/gcc-11.5.0/bin/g++\x1b[24;39m')
  })

  it('highlights a path containing @ and ~', () => {
    const input = 'load /tmp/cache@1.tgz and ~/proj-2/app.exe now\n'
    const out = highlight(input)
    expect(out).toContain('\x1b[35m/tmp/cache@1.tgz\x1b[24;39m')
    expect(out).toContain('\x1b[35m~/proj-2/app.exe\x1b[24;39m')
  })

  it('does not treat a bare an+b expression as a path', () => {
    const input = 'compute an+b + c then d+e\n'
    const out = highlight(input)
    // Not starting with `/` or `~/` — must not match the path pattern.
    expect(out).not.toContain('\x1b[35m')
  })

  it('highlights timestamps in bright blue', () => {
    expect(highlight('at 12:34:56 done\n')).toContain('\x1b[94m12:34:56\x1b[24;39m')
  })

  it('highlights quoted strings in yellow', () => {
    expect(highlight('msg "hello world" end\n')).toContain('\x1b[33m"hello world"\x1b[24;39m')
  })

  it('highlights braces in plain prose', () => {
    const out = highlight('hello {world}')
    expect(out).toContain('\x1b[95m') // brace colour from palette
    expect(out).not.toBe('hello {world}')
  })
})

describe('highlight() — safety guards', () => {
  it('skips content INSIDE a fenced code block', () => {
    const input = '```js\nconst x = {a: 1}\necho "hello {world}"\n```\n'
    const out = highlight(input)
    // The fence lines and the two content lines must pass through
    // untouched — no SGR inserted between the backticks.
    expect(out).toBe(input)
  })

  it('skips content inside a tilde fence', () => {
    const input = '~~~py\nprint("{x}")\n~~~\n'
    expect(highlight(input)).toBe(input)
  })

  it('tolerates an info string on the opening fence', () => {
    const input = '```javascript\nlet {a, b} = obj\n```\n'
    expect(highlight(input)).toBe(input)
  })

  it('only closes a fence when the same character is used', () => {
    // ``` must close ``` and not ~~~ (CommonMark rule, kept simple here)
    const input = '```\n{not highlighted}\n~~~\n{still inside fence — not highlighted}\n```\n'
    const out = highlight(input)
    expect(out).toBe(input)
  })

  it('skips indented code lines (4+ leading spaces): braces stay plain, keywords still colored', () => {
    const input = '    log error {details}\n'
    const out = highlight(input)
    // No brace (bright magenta) colour injected on the indented line.
    expect(out).not.toContain('\x1b[95m')
    // Non-brace tokens still get highlighted (the error word becomes red).
    expect(out).toContain('\x1b[31merror\x1b[24;39m')
  })

  it('highlights the IP in 4-space-indented `ip a` output', () => {
    const input = '    inet 10.1.0.13/24 brd 10.1.0.255 scope global dynamic eth0\n'
    const out = highlight(input)
    expect(out).toContain('\x1b[35m10.1.0.13\x1b[24;39m')
    expect(out).toContain('\x1b[35m10.1.0.255\x1b[24;39m')
  })

  it('still highlights prose after a fence closes', () => {
    const input = '```\n{x}\n```\nplain {prose}\n'
    const out = highlight(input)
    // The brace palette opener must precede the brace and the reset must
    // follow the brace's content — verify both ends rather than the full
    // byte sequence (colour codes can wrap around braces independently).
    expect(out).toContain('\x1b[95m{')
    expect(out).toContain('}\x1b[24;39m')
    // The fenced {x} line must NOT carry the brace colour.
    expect(out).not.toContain('\x1b[95m{x}')
  })

  it('passes SGR-only lines through unchanged when no plain braces are present', () => {
    // highlight() splits on CSI boundaries and only colours plain segments
    // inside an SGR-only line — so a line with no plain braces is unchanged.
    const input = '\x1b[31mred text\x1b[0m\n'
    expect(highlight(input)).toBe(input)
  })

  it('skips already-colored spans instead of fragmenting them', () => {
    // `ls` colors a directory blue; the app must NOT re-color its content.
    const input = '\x1b[01;34mchatGLM2-6B\x1b[0m\n'
    expect(highlight(input)).toBe(input)
  })

  it('skips an extended-256-color span like a plain one', () => {
    const input = '\x1b[38;5;39mport8-api\x1b[0m\n'
    expect(highlight(input)).toBe(input)
  })

  it('still highlights default-colored text after and around a colored span', () => {
    // "error" sits outside the colored span, so it must still be styled.
    const input = '\x1b[32mOK\x1b[0m error\n'
    const out = highlight(input)
    expect(out).toContain('\x1b[31merror')
    expect(out).toContain('\x1b[32mOK\x1b[0m ')
  })

  it('handles multiple fences interleaved with prose', () => {
    const input = 'before {x}\n```\n{inside}\n```\nafter {y}\n'
    const out = highlight(input)
    expect(out).toContain('before ')
    expect(out).toContain('after ')
    // The {x} and {y} get the brace colour; {inside} does not.
    expect(out.split('\x1b[95m').length - 1).toBeGreaterThanOrEqual(2)
  })

  it('is stable when given an empty string', () => {
    expect(highlight('')).toBe('')
  })

  it('is stable when given only a fence pair', () => {
    expect(highlight('```\n```\n')).toBe('```\n```\n')
  })
})
