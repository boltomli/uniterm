import { describe, it, expect } from 'vitest'
import { parseCastContent, type ParsedCast } from '../src/services/terminalRecorder'

/**
 * parseCastContent 解析 asciinema cast v2 文件内容为可回放的事件序列。
 * 这些测试覆盖 header 解析、事件提取、duration 计算、无效行容错与边界情况，
 * 不依赖 Wails binding（parseCastContent 是纯函数）。
 */

// 构造一个标准 cast v2 header 行
function headerLine(width = 80, height = 24, timestamp = 1700000000): string {
  return JSON.stringify({ version: 2, width, height, timestamp, env: { SHELL: '/bin/bash', TERM: 'xterm-256color' } })
}

// 构造一个事件行 [elapsed, type, data]
function eventLine(time: number, type: 'o' | 'i', data: string): string {
  return JSON.stringify([time, type, data])
}

describe('parseCastContent', () => {
  it('解析标准 cast v2 内容：header + 多个输出事件', () => {
    const content = [
      headerLine(120, 40),
      eventLine(0.5, 'o', 'Hello'),
      eventLine(1.2, 'o', ' World\n'),
      eventLine(2.0, 'o', '$ '),
    ].join('\n')
    const result = parseCastContent(content)

    expect(result.header).not.toBeNull()
    expect(result.header!.version).toBe(2)
    expect(result.header!.width).toBe(120)
    expect(result.header!.height).toBe(40)
    expect(result.header!.timestamp).toBe(1700000000)
    expect(result.header!.env?.SHELL).toBe('/bin/bash')
    expect(result.header!.env?.TERM).toBe('xterm-256color')

    expect(result.events).toHaveLength(3)
    expect(result.events[0]).toEqual({ time: 0.5, type: 'o', data: 'Hello' })
    expect(result.events[1]).toEqual({ time: 1.2, type: 'o', data: ' World\n' })
    expect(result.events[2]).toEqual({ time: 2.0, type: 'o', data: '$ ' })

    // duration 为最后一帧（最大 time）
    expect(result.duration).toBe(2.0)
  })

  it('解析包含输入事件（"i" 类型）的内容', () => {
    const content = [
      headerLine(),
      eventLine(0.1, 'o', 'prompt$ '),
      eventLine(0.3, 'i', 'ls'),
      eventLine(0.35, 'i', '\r'),
      eventLine(0.5, 'o', 'file.txt\n'),
    ].join('\n')
    const result = parseCastContent(content)

    expect(result.events).toHaveLength(4)
    expect(result.events[1].type).toBe('i')
    expect(result.events[1].data).toBe('ls')
    expect(result.events[2].type).toBe('i')
    expect(result.events[2].data).toBe('\r')
    expect(result.duration).toBe(0.5)
  })

  it('解析空字符串返回空结果', () => {
    const result = parseCastContent('')
    expect(result.header).toBeNull()
    expect(result.events).toHaveLength(0)
    expect(result.duration).toBe(0)
  })

  it('解析只有 header 无事件的内容', () => {
    const content = headerLine(80, 24)
    const result = parseCastContent(content)
    expect(result.header).not.toBeNull()
    expect(result.header!.width).toBe(80)
    expect(result.events).toHaveLength(0)
    expect(result.duration).toBe(0)
  })

  it('跳过无法解析的无效行，保留有效事件', () => {
    const content = [
      headerLine(),
      eventLine(0.5, 'o', 'valid'),
      'this is not json',
      '{"not":"an array"}',
      '[1.0, "o"]', // 缺少 data 字段（长度 < 3）
      '["not_number", "o", "data"]', // 第一个元素不是 number
      eventLine(1.5, 'o', 'also valid'),
    ].join('\n')
    const result = parseCastContent(content)

    expect(result.events).toHaveLength(2)
    expect(result.events[0].data).toBe('valid')
    expect(result.events[1].data).toBe('also valid')
    expect(result.duration).toBe(1.5)
  })

  it('duration 取所有事件中的最大 time（非最后一行）', () => {
    const content = [
      headerLine(),
      eventLine(5.0, 'o', 'late'),
      eventLine(1.0, 'o', 'early'),
      eventLine(3.0, 'o', 'middle'),
    ].join('\n')
    const result = parseCastContent(content)

    expect(result.duration).toBe(5.0)
    // 事件按出现顺序保留（不排序）
    expect(result.events[0].time).toBe(5.0)
    expect(result.events[1].time).toBe(1.0)
    expect(result.events[2].time).toBe(3.0)
  })

  it('处理带空行和首尾空白的内容', () => {
    const content = [
      '',
      headerLine(100, 30),
      '',
      eventLine(0.2, 'o', 'data'),
      '',
      '',
    ].join('\n')
    const result = parseCastContent(content)

    // 第一行为空，header 在第二个非空行（以 { 开头）
    expect(result.header).not.toBeNull()
    expect(result.header!.width).toBe(100)
    expect(result.events).toHaveLength(1)
    expect(result.events[0].data).toBe('data')
    expect(result.duration).toBe(0.2)
  })

  it('处理非 JSON header 行（跳过，继续解析事件）', () => {
    const content = [
      'not a json header',
      eventLine(0.5, 'o', 'event without header'),
    ].join('\n')
    const result = parseCastContent(content)

    // 第一行不是 JSON，header 为 null
    expect(result.header).toBeNull()
    // 但事件仍被解析（第一行被当作 header 尝试解析失败后跳过）
    expect(result.events).toHaveLength(1)
    expect(result.events[0].data).toBe('event without header')
  })

  it('处理包含特殊字符的终端输出数据', () => {
    const specialData = '\x1b[31mred text\x1b[0m\t\n\r\\'
    const content = [
      headerLine(),
      eventLine(0.1, 'o', specialData),
    ].join('\n')
    const result = parseCastContent(content)

    expect(result.events).toHaveLength(1)
    expect(result.events[0].data).toBe(specialData)
  })

  it('header 包含 env 为空对象时正确解析', () => {
    const content = [
      JSON.stringify({ version: 2, width: 80, height: 24, timestamp: 0, env: {} }),
      eventLine(1.0, 'o', 'test'),
    ].join('\n')
    const result = parseCastContent(content)

    expect(result.header).not.toBeNull()
    expect(result.header!.env).toEqual({})
    expect(result.events).toHaveLength(1)
  })

  it('多帧大量事件正确累计 duration', () => {
    const lines = [headerLine()]
    const count = 100
    for (let i = 1; i <= count; i++) {
      lines.push(eventLine(i * 0.1, 'o', `frame ${i}`))
    }
    const result = parseCastContent(lines.join('\n'))

    expect(result.events).toHaveLength(count)
    expect(result.duration).toBeCloseTo(count * 0.1, 5)
  })
})