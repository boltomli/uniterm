import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'

// P-05: the overlay highlighter used to re-run the regex pass over every
// visible row on every ~10ms write refresh (screen rows were never cached).
// Now screen rows are cached with their stitched text and only rescanned
// when the text actually changed, and sustained output widens the sync-scan
// gate from 10ms to 33ms.

const { matchTextSpans } = vi.hoisted(() => ({ matchTextSpans: vi.fn() }))

vi.mock('./highlightRules', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./highlightRules')>()
  return { ...actual, matchTextSpans: (...args: Parameters<typeof actual.matchTextSpans>) => {
    matchTextSpans(...args)
    return actual.matchTextSpans(...args)
  } }
})
vi.mock('../stores/settingsStore', () => ({
  useSettingsStore: () => ({ settings: { terminal: { highlightEnabled: true } } }),
}))

import { OverlayHighlighter } from './overlayHighlight'

function makeLine(text: string, wrapped = false) {
  return {
    isWrapped: wrapped,
    translateToString: (_trim?: boolean, start?: number, end?: number) =>
      text.slice(start ?? 0, end ?? text.length),
  }
}

/** Minimal xterm stand-in: a normal buffer of plain (unwrapped) rows. */
function makeTerm(rows: string[]) {
  const lines = rows.map(r => makeLine(r))
  const buffer = {
    type: 'normal' as const,
    get length() { return lines.length },
    baseY: 0,
    viewportY: 0,
    cursorY: 0,
    getLine: (y: number) => lines[y],
    getNullCell: () => ({}),
  }
  const term = {
    rows: rows.length,
    cols: 80,
    buffer: { active: buffer },
    options: { theme: {} },
    onWriteParsed: () => ({ dispose() {} }),
    onResize: () => ({ dispose() {} }),
    onRender: () => ({ dispose() {} }),
    setCellColorOverrides: vi.fn(),
    clearCellColorOverrides: vi.fn(),
    registerMarker: () => null,
  }
  return { term, lines }
}

const SAMPLE_ROWS = [
  'ERROR: disk full at /var/log',
  'info: build finished in 3s',
  'https://example.com/docs',
  'plain row with nothing',
  'WARN: retrying connection',
]

describe('OverlayHighlighter screen-row text cache (P-05)', () => {
  beforeEach(() => {
    matchTextSpans.mockClear()
    // node has no rAF (the budget continuation would throw and kill the
    // highlighter), and a constant clock keeps the 3ms scan budget from
    // tripping on a cold start — both deterministic in a browser.
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) =>
      setTimeout(() => cb(0), 16) as unknown as number)
    vi.stubGlobal('cancelAnimationFrame', (id: number) => clearTimeout(id as unknown as ReturnType<typeof setTimeout>))
    vi.spyOn(performance, 'now').mockReturnValue(1_000)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('does not re-run matchTextSpans for unchanged screen rows', () => {
    const { term } = makeTerm(SAMPLE_ROWS)
    const h = new OverlayHighlighter(term as never)

    h.attach() // first refresh: every logical line scanned once
    expect(matchTextSpans).toHaveBeenCalledTimes(SAMPLE_ROWS.length)

    // Same content, second refresh: all cache hits, zero regex work —
    // but the overrides are still re-synced so stale colors clear.
    term.setCellColorOverrides.mockClear()
    ;(h as unknown as { refreshViewport(): void }).refreshViewport()
    expect(matchTextSpans).toHaveBeenCalledTimes(SAMPLE_ROWS.length)
    expect(term.setCellColorOverrides).toHaveBeenCalledTimes(SAMPLE_ROWS.length)

    h.dispose()
  })

  it('rescans exactly the rows whose text changed', () => {
    const { term, lines } = makeTerm(SAMPLE_ROWS)
    const h = new OverlayHighlighter(term as never)
    h.attach()
    matchTextSpans.mockClear()

    lines[2] = makeLine('https://changed.example.org/new')
    ;(h as unknown as { refreshViewport(): void }).refreshViewport()

    expect(matchTextSpans).toHaveBeenCalledTimes(1)
    h.dispose()
  })

  it('revalidates when a row gains content in place', () => {
    const { term, lines } = makeTerm(SAMPLE_ROWS)
    const h = new OverlayHighlighter(term as never)
    h.attach()
    matchTextSpans.mockClear()

    lines[3] = makeLine('plain row with nothing ERROR: now it matches')
    ;(h as unknown as { refreshViewport(): void }).refreshViewport()

    expect(matchTextSpans).toHaveBeenCalledTimes(1)
    h.dispose()
  })
})

describe('OverlayHighlighter flood backoff (P-05)', () => {
  let nowSpy: { mockReturnValue(v: number): void; mockRestore(): void }

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'requestAnimationFrame', 'cancelAnimationFrame'] })
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) =>
      setTimeout(() => cb(0), 16) as unknown as number)
    vi.stubGlobal('cancelAnimationFrame', (id: number) => clearTimeout(id as unknown as ReturnType<typeof setTimeout>))
    nowSpy = vi.spyOn(performance, 'now')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it('widens the sync-scan gate under sustained output and restores it on idle', () => {
    const { term } = makeTerm(SAMPLE_ROWS)
    const h = new OverlayHighlighter(term as never)
    const refresh = vi.fn()
    ;(h as unknown as { refreshViewport(): void }).refreshViewport = refresh
    nowSpy.mockReturnValue(0)
    h.attach()
    expect(refresh).toHaveBeenCalledTimes(1) // t=0: sync scan #1

    // Writes every 15ms: within the 10ms gate initially, so each is a sync
    // scan; after 5 consecutive in-window scans the gate widens to 33ms.
    nowSpy.mockReturnValue(15); (h as unknown as { triggerWriteRefresh(): void }).triggerWriteRefresh()
    expect(refresh).toHaveBeenCalledTimes(2)
    nowSpy.mockReturnValue(30); (h as unknown as { triggerWriteRefresh(): void }).triggerWriteRefresh()
    expect(refresh).toHaveBeenCalledTimes(3)
    nowSpy.mockReturnValue(45); (h as unknown as { triggerWriteRefresh(): void }).triggerWriteRefresh()
    expect(refresh).toHaveBeenCalledTimes(4)
    nowSpy.mockReturnValue(60); (h as unknown as { triggerWriteRefresh(): void }).triggerWriteRefresh()
    expect(refresh).toHaveBeenCalledTimes(5) // 5th consecutive → flood mode

    // 15ms later: below the widened 33ms gate → trailing debounce only,
    // no synchronous scan.
    nowSpy.mockReturnValue(75); (h as unknown as { triggerWriteRefresh(): void }).triggerWriteRefresh()
    expect(refresh).toHaveBeenCalledTimes(5)

    // Past the widened gate: sync scans resume at the slower cadence.
    nowSpy.mockReturnValue(108); (h as unknown as { triggerWriteRefresh(): void }).triggerWriteRefresh()
    expect(refresh).toHaveBeenCalledTimes(6)

    // A gap longer than WRITE_MAX_WAIT_MS (500ms) means output stopped:
    // the tight 10ms gate is restored.
    nowSpy.mockReturnValue(708); (h as unknown as { triggerWriteRefresh(): void }).triggerWriteRefresh()
    expect(refresh).toHaveBeenCalledTimes(7)

    h.dispose()
  })
})
