// Lazy indirection for waitForTerminalSize.
//
// waitForTerminalSize lives in services/terminalManager, whose module graph
// pulls in the entire xterm stack (xterm + WebGL/image/search/unicode addons,
// ~730 kB minified). App.vue and useDuplicateSession only ever need the size
// helper — and only inside session-start handlers — so importing it through
// here keeps that stack in its own chunk: it loads when a terminal actually
// starts instead of before first paint.
//
// Everything that renders a terminal (BaseTerminal, Panel, TerminalTabContent)
// still imports terminalManager directly; they are all behind async tab
// components, so they are already off the startup path.
type WaitForTerminalSize = (typeof import('./terminalManager'))['waitForTerminalSize']

export const waitForTerminalSize: WaitForTerminalSize = (...args) =>
  import('./terminalManager').then((m) => m.waitForTerminalSize(...args))
