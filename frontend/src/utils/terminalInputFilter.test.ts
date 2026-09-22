import { describe, expect, it } from 'vitest'
import { filterTerminalInput } from './terminalInputFilter'

describe('filterTerminalInput()', () => {
  it('preserves primary and secondary Device Attributes responses', () => {
    expect(filterTerminalInput('\x1b[?1;2c', false)).toBe('\x1b[?1;2c')
    expect(filterTerminalInput('\x1b[>0;276;0c', false)).toBe('\x1b[>0;276;0c')
  })

  it('preserves the cursor-position report survey-class prompts wait for', () => {
    // AlecAivazis/survey (docker compose y/N) sends ESC[6n and blocks forever
    // until the CPR reply ESC[<row>;<col>R arrives on stdin.
    expect(filterTerminalInput('\x1b[24;80R', false)).toBe('\x1b[24;80R')
    expect(filterTerminalInput('\x1b[1;1R', true)).toBe('\x1b[1;1R')
  })

  it('preserves device-status and window-size responses', () => {
    expect(filterTerminalInput('\x1b[0n', false)).toBe('\x1b[0n')
    expect(filterTerminalInput('\x1b[8;24;80t', false)).toBe('\x1b[8;24;80t')
  })

  it('strips focus responses only outside the alternate screen', () => {
    expect(filterTerminalInput('a\x1b[Ib\x1b[Oc', false)).toBe('abc')
    expect(filterTerminalInput('a\x1b[Ib\x1b[Oc', true)).toBe('a\x1b[Ib\x1b[Oc')
  })

  it('strips OSC responses', () => {
    expect(filterTerminalInput('a\x1b]11;rgb:0/0/0\x07b', false)).toBe('ab')
    expect(filterTerminalInput('a\x1b]10;rgb:1/1/1\x1b\\b', false)).toBe('ab')
  })

  it('preserves ordinary keyboard input around filtered responses', () => {
    expect(filterTerminalInput('hello\x1b[I world', false)).toBe('hello world')
  })
})
