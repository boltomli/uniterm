// Regression tests for P1: MCP approval requests must be shown one at a
// time, each keyed by its unique id. Previously App.vue overwrote
// `mcpApprovalRequest` on every `mcp:approval-request` event, so a second
// request replaced the dialog the user was reading (and inherited its
// countdown) while Approve answered whatever arrived last.

import { describe, expect, it } from 'vitest'
import { MCPApprovalQueue } from './mcpApprovalQueue'
import type { MCPApprovalRequest } from '../types/mcp'

function req(id: string): MCPApprovalRequest {
  return { id, client: 'claude', connection: 'web-01', command: `cmd-${id}` }
}

describe('MCPApprovalQueue', () => {
  it('shows the first request immediately', () => {
    const q = new MCPApprovalQueue()
    expect(q.enqueue(req('a'))).toEqual(req('a'))
    expect(q.currentRequest?.id).toBe('a')
  })

  it('keeps the first request on screen while a second waits (no swap)', () => {
    const q = new MCPApprovalQueue()
    q.enqueue(req('a'))
    // The second request must NOT become the displayed one.
    expect(q.enqueue(req('b'))).toEqual(req('a'))
    expect(q.currentRequest?.id).toBe('a')
  })

  it('advances to the queued request only after the current one is resolved', () => {
    const q = new MCPApprovalQueue()
    q.enqueue(req('a'))
    q.enqueue(req('b'))

    const next = q.resolve('a')
    expect(next?.id).toBe('b')
    expect(q.currentRequest?.id).toBe('b')
  })

  it('drains to null when the queue empties', () => {
    const q = new MCPApprovalQueue()
    q.enqueue(req('a'))
    expect(q.resolve('a')).toBeNull()
    expect(q.currentRequest).toBeNull()
  })

  it('never lets a stale id resolve a different request', () => {
    const q = new MCPApprovalQueue()
    q.enqueue(req('a'))
    q.enqueue(req('b'))
    q.resolve('a')
    // A late click carrying the OLD id must not answer request b.
    expect(q.resolve('a')?.id).toBe('b')
    expect(q.currentRequest?.id).toBe('b')
    expect(q.resolve('b')).toBeNull()
  })

  it('drops duplicate events by id (redelivery must not reset the dialog)', () => {
    const q = new MCPApprovalQueue()
    q.enqueue(req('a'))
    expect(q.enqueue(req('a'))).toEqual(req('a'))
    // Duplicate of a QUEUED request is dropped too.
    q.enqueue(req('b'))
    q.enqueue(req('b'))
    expect(q.resolve('a')?.id).toBe('b')
    expect(q.resolve('b')).toBeNull()
  })

  it('ignores a request without an id', () => {
    const q = new MCPApprovalQueue()
    const broken = { id: '', client: 'x' } as MCPApprovalRequest
    expect(q.enqueue(broken)).toBeNull()
    expect(q.currentRequest).toBeNull()
  })

  it('accepts a new request after the queue drained', () => {
    const q = new MCPApprovalQueue()
    q.enqueue(req('a'))
    q.resolve('a')
    expect(q.enqueue(req('b'))).toEqual(req('b'))
    expect(q.currentRequest?.id).toBe('b')
  })
})
