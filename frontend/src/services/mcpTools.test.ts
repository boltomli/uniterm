// Unit tests for the external MCP client tool adapter. The Go bindings are
// mocked away here; only the name-spacing and schema adaptation are exercised.

import { describe, expect, it, vi } from 'vitest'

vi.mock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
  ListMCPTools: async () => [
    {
      server: 'fs',
      name: 'read_file',
      qualifiedName: 'mcp__fs__read_file',
      description: 'Read a file',
      inputSchema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
    },
  ],
  CallMCPTool: async () => 'echo',
}))

import {
  isRemoteMCPTool,
  loadRemoteMCPTools,
  toAgentToolEntry,
} from './mcpTools'

describe('mcpTools adapter', () => {
  it('detects namespaced remote tools', () => {
    expect(isRemoteMCPTool('mcp__fs__read_file')).toBe(true)
    expect(isRemoteMCPTool('execute_command')).toBe(false)
  })

  it('converts a remote tool to the agent entry shape', async () => {
    const tools = await loadRemoteMCPTools()
    expect(tools).toHaveLength(1)
    const entry = toAgentToolEntry(tools[0])
    expect(entry.name).toBe('mcp__fs__read_file')
    expect(entry.input_schema.properties).toHaveProperty('path')
    expect(entry.input_schema.required).toEqual(['path'])
    expect(entry.description).toContain('fs')
  })

  it('degrades to an empty list when the binding errors', async () => {
    vi.resetModules()
    vi.doMock('../../bindings/github.com/ys-ll/uniterm/app', () => ({
      ListMCPTools: async () => { throw new Error('offline') },
    }))
    const mod = await import('./mcpTools')
    const list = await mod.loadRemoteMCPTools()
    expect(list).toEqual([])
  })
})
