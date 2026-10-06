// Filter terminal-generated input that the remote does not want echoed back.
// Query responses (CPR cursor-position, DSR status, DA device-attributes,
// window ops) must pass through: the remote only ever sees xterm generate
// them in reply to a query IT sent, so an app that queries is waiting for
// the answer — stripping it blocks the app forever. AlecAivazis/survey (the
// y/N prompt library behind docker compose) reads ESC[6n's reply in a
// blocking loop; stripping it froze the whole terminal. fish similarly waits
// for DA replies during startup.
export function filterTerminalInput(input: string, inAlternateScreen: boolean): string {
  // OSC responses: ESC ] ... BEL or ESC ] ... ESC \
  let filtered = input.replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '')
  if (inAlternateScreen) return filtered
  // Normal screen only: also strip focus in/out, which a shell does not want.
  return filtered.replace(/\x1b\[(?:[?>][\d;]*|[\d;]*)([IO])/g, '')
}

// Remove xterm's auto-generated device reports (CPR `ESC[<r>;<c>R`, DSR
// `ESC[0n`, DA `ESC[?...c`). Unlike filterTerminalInput these must NOT pass
// through when they are stale: during a KeepAlive gap replay the visible
// terminal re-parses queries the mirror already answered, and a second reply
// arriving after the remote's read timed out leaks its tail into the shell
// (the "0R" artifact). No keystroke can produce these shapes, so the strip is
// safe even if it overlaps real typing.
export function stripDeviceReplies(input: string): string {
  return input.replace(/\x1b\[[0-9;?><]*[Rnc]/g, '')
}
