import { describe, it, expect } from 'vitest'
import { pickGhostSuggestion } from './terminalGhostText'

const history = (value: string) => ({ type: 'history', value })
const quick = (value: string) => ({ type: 'quick-command', value })

describe('pickGhostSuggestion', () => {
  it('hints the tail of the first continuing suggestion', () => {
    const items = [history('ls -la'), history('ls -lh')]
    const hit = pickGhostSuggestion(items, 'ls ')
    expect(hit?.value).toBe('ls -la')
    expect(hit!.value.slice('ls '.length)).toBe('-la')
  })

  it('takes quick commands too, in list order', () => {
    const items = [quick('docker compose up -d'), history('docker ps')]
    expect(pickGhostSuggestion(items, 'docker ')?.value).toBe('docker compose up -d')
  })

  it('ignores suggestions that do not continue the typed text', () => {
    // Fuzzy matches rank into the same list ("gst" matches "git status"), but
    // only a literal prefix has a tail that can be drawn after the cursor.
    const items = [history('git status'), history('grep -rn status .')]
    expect(pickGhostSuggestion(items, 'gst')).toBeNull()
  })

  it('is case-sensitive so the hint cannot rewrite what was typed', () => {
    // History matching is case-insensitive, so this item really does reach the
    // popup. Hinting "atus" after "GIT st" would read as "GIT status".
    expect(pickGhostSuggestion([history('git status')], 'GIT st')).toBeNull()
  })

  it('hints nothing once the line already equals the suggestion', () => {
    // This is what lets a second Tab fall through to the remote shell.
    expect(pickGhostSuggestion([history('git status')], 'git status')).toBeNull()
  })

  it('skips AI items', () => {
    // ai-preview carries no command; ai-result lands seconds later and would
    // flicker the hint in after the user has moved on.
    const items = [
      { type: 'ai-preview', value: '' },
      { type: 'ai-result', value: 'git status --short' },
    ]
    expect(pickGhostSuggestion(items, 'git s')).toBeNull()
  })

  it('hints nothing on an empty line', () => {
    expect(pickGhostSuggestion([history('git status')], '')).toBeNull()
  })
})
