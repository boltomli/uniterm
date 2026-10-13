// MCP approval queue (P1: 审批对话框请求被覆盖).
//
// The backend serializes approval dialogs (app_mcp.go mcpApprove holds
// mcpApprovalSerial), but the renderer must not trust that alone: event
// delivery can still be out of order or duplicated, and a stale "approve"
// click must never answer a different request than the one on screen.
//
// Every request carries a unique id. The queue shows exactly one request at a
// time; Approve/Deny resolves the CURRENT id only, then the next queued
// request (if any) becomes current — so the dialog the user is reading is
// never swapped underneath them.

import type { MCPApprovalRequest } from '../types/mcp'

export class MCPApprovalQueue {
  private current: MCPApprovalRequest | null = null
  private readonly pending: MCPApprovalRequest[] = []

  /**
   * Enqueue a request and return the one to display now: the new request
   * when the dialog is idle, otherwise the request already on screen (the
   * new one waits its turn). Duplicate ids are dropped — a redelivered event
   * must neither reset the countdown nor re-enter the queue.
   */
  enqueue(req: MCPApprovalRequest): MCPApprovalRequest | null {
    if (!req?.id) return this.current
    if (this.current?.id === req.id) return this.current
    if (this.pending.some(p => p.id === req.id)) return this.current
    if (!this.current) {
      this.current = req
      return req
    }
    this.pending.push(req)
    return this.current
  }

  /** The request the dialog is showing — the only one Approve may answer. */
  get currentRequest(): MCPApprovalRequest | null {
    return this.current
  }

  /**
   * Resolve the displayed request by id and advance to the next one.
   * Returns the request to display next, or null when the queue drains.
   * A mismatched id resolves nothing and keeps the current request — a late
   * click for an already-answered dialog can never answer its successor.
   */
  resolve(id: string): MCPApprovalRequest | null {
    if (this.current?.id !== id) return this.current
    this.current = this.pending.shift() ?? null
    return this.current
  }

  /** Drop everything (window teardown). */
  clear(): void {
    this.current = null
    this.pending.length = 0
  }
}
