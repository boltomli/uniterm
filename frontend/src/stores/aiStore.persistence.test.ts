// Regression tests for security.md「前端核心」:
//   correctness — deleteMessage/truncateFrom only spliced the VIEW array
//     (messages.value); the owning session's messages array was untouched, so
//     deleted messages resurrected after switchSession (which rebuilds the view
//     as copies).
//   perf — doSave re-serialized every session on every message; it is now a
//     500ms trailing debounce that only re-serializes dirty sessions (clean
//     sessions reuse their cached payload entry).
//
// NOTE on payload shape: the payload still carries ALL sessions, not just the
// dirty ones — backend AISessionStore.Save removes shard files that are absent
// from the payload (backend/store/ai_session_store.go:99-121), so a dirty-only
// payload would delete the clean sessions from disk. "Only serialize dirty"
// means the expensive per-message mapping is skipped for clean sessions (their
// cached entry object is reused verbatim, which the identity assertions below
// lock in).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

const { mockSaveAISessions } = vi.hoisted(() => ({
  mockSaveAISessions: vi.fn().mockResolvedValue(undefined),
}))

vi.mock('@wailsio/runtime', () => ({
  Events: { On: vi.fn(() => () => {}), Off: vi.fn() },
}))

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  LoadAISessions: vi.fn().mockResolvedValue({ sessions: [], currentSessionId: '' }),
  SaveAISessions: mockSaveAISessions,
  DisableSessionOutputLog: vi.fn().mockResolvedValue(undefined),
  RegisterSessionForPanel: vi.fn().mockResolvedValue(undefined),
  UnregisterSession: vi.fn().mockResolvedValue(undefined),
  LoadLocalState: vi.fn().mockResolvedValue({}),
  SaveLocalState: vi.fn().mockResolvedValue(undefined),
}))

import { useAIStore } from './aiStore'
import type { AIMessage } from '../types/ai'

const SAVE_DEBOUNCE_MS = 500

function msg(id: string, role: AIMessage['role'], content: string): AIMessage {
  return { id, role, content } as AIMessage
}

/** Advance fake time past the save debounce and flush microtasks. */
async function flushSave() {
  await vi.advanceTimersByTimeAsync(SAVE_DEBOUNCE_MS)
}

function lastPayload() {
  expect(mockSaveAISessions).toHaveBeenCalled()
  const calls = mockSaveAISessions.mock.calls
  return calls[calls.length - 1][0] as {
    sessions: Array<{ id: string; name: string; messages: Array<{ id: string }> }>
    currentSessionId: string
  }
}

describe('AI message deletion is persisted (security.md FE-Medium)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('deleteMessage stays deleted after switching away and back', async () => {
    const ai = useAIStore()
    ai.createSession()
    const first = ai.currentSessionId!
    ai.addMessage(msg('m1', 'user', 'hello'))
    ai.addMessage(msg('m2', 'assistant', 'world'))
    await flushSave()

    ai.deleteMessage('m2')
    expect(ai.messages.map(m => m.id)).toEqual(['m1'])

    // View and session array must both lose m2: switchSession rebuilds the
    // view from s.messages, so a view-only splice resurrects the message.
    ai.createSession()
    ai.switchSession(first)
    expect(ai.messages.map(m => m.id)).toEqual(['m1'])

    // And the persisted payload must not contain it either.
    await flushSave()
    const persisted = lastPayload().sessions.find(s => s.id === first)
    expect(persisted?.messages.map(m => m.id)).toEqual(['m1'])
  })

  it('truncateFrom keeps everything after the cut point gone after a round-trip', async () => {
    const ai = useAIStore()
    ai.createSession()
    const first = ai.currentSessionId!
    ai.addMessage(msg('m1', 'user', 'a'))
    ai.addMessage(msg('m2', 'assistant', 'b'))
    ai.addMessage(msg('m3', 'user', 'c'))

    ai.truncateFrom('m2')
    expect(ai.messages.map(m => m.id)).toEqual(['m1'])

    // createSession derives its id from Date.now(); under fake timers that is
    // frozen, so tick before creating the second session or both get one id.
    await vi.advanceTimersByTimeAsync(1)
    ai.createSession()
    ai.switchSession(first)
    expect(ai.messages.map(m => m.id)).toEqual(['m1'])

    await flushSave()
    const persisted = lastPayload().sessions.find(s => s.id === first)
    expect(persisted?.messages.map(m => m.id)).toEqual(['m1'])
  })
})

describe('doSave debounce + dirty-only serialization (security.md FE-Medium)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('collapses 10 rapid messages into a single save', async () => {
    const ai = useAIStore()
    ai.createSession()
    for (let i = 0; i < 10; i++) ai.addMessage(msg(`m${i}`, 'user', `line ${i}`))

    expect(mockSaveAISessions).not.toHaveBeenCalled()
    await flushSave()

    expect(mockSaveAISessions).toHaveBeenCalledTimes(1)
    expect(lastPayload().sessions[0].messages).toHaveLength(10)
  })

  it('re-serializes only the dirty session; clean sessions reuse their cached entry', async () => {
    const ai = useAIStore()
    ai.createSession()
    const clean = ai.currentSessionId!
    ai.addMessage(msg('c1', 'user', 'clean session message'))
    await flushSave()
    expect(mockSaveAISessions).toHaveBeenCalledTimes(1)
    const cleanEntryInSave1 = lastPayload().sessions.find(s => s.id === clean)

    // New current session; mutating it must not re-serialize the clean one.
    ai.createSession()
    const dirty = ai.currentSessionId!
    ai.addMessage(msg('d1', 'user', 'dirty session message'))
    await flushSave()
    expect(mockSaveAISessions).toHaveBeenCalledTimes(2)

    const save2 = lastPayload()
    const cleanEntryInSave2 = save2.sessions.find(s => s.id === clean)
    const dirtyEntry = save2.sessions.find(s => s.id === dirty)
    // Same object identity ⇒ the clean session was served from the cache and
    // its messages were never re-mapped.
    expect(cleanEntryInSave2).toBe(cleanEntryInSave1)
    // The dirty session got a fresh serialization.
    expect(dirtyEntry).not.toBeUndefined()
    expect(dirtyEntry?.messages.map(m => m.id)).toEqual(['d1'])
  })

  it('renaming a non-current session marks that session dirty', async () => {
    const ai = useAIStore()
    ai.createSession()
    const a = ai.currentSessionId!
    ai.addMessage(msg('a1', 'user', 'a'))
    await flushSave()
    const before = lastPayload().sessions.find(s => s.id === a)

    ai.createSession()
    const b = ai.currentSessionId!
    ai.renameSession(a, 'renamed')
    await flushSave()

    const after = lastPayload().sessions.find(s => s.id === a)
    expect(after).not.toBe(before)
    expect(after?.id).toBe(a)
    expect(lastPayload().sessions.find(s => s.id === b)).toBeDefined()
  })
})
