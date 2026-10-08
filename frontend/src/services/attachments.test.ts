// Tests for the AI attachment service: whitelist / size / count validation and
// the Anthropic content blocks the backend converts to each provider's shape.
// Everything here is offline — no API keys, no network.

import { describe, expect, it } from 'vitest'
import {
  ATTACHMENT_FILE_ACCEPT,
  ATTACHMENT_LIMITS,
  attachmentToAnthropicBlock,
  buildUserContent,
  classifyAttachment,
  createImageAttachment,
  createImageAttachmentFromDataURL,
  createTextAttachment,
  dataUrlPayload,
  fileExtension,
  formatBytes,
  formatTextAttachment,
  redactAttachmentsForLog,
  validateAttachment,
} from './attachments'
import type { AIAttachment } from '../types/ai'

const PNG_BASE64 =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='

function image(size = 1024, name = 'shot.png'): AIAttachment {
  return createImageAttachment(name, 'image/png', PNG_BASE64, size)
}

function textFile(name = 'app.log', content = 'boom'): AIAttachment {
  return createTextAttachment(name, 'text/plain', content, content.length)
}

describe('classifyAttachment', () => {
  it('accepts the whitelisted image types', () => {
    expect(classifyAttachment('a.png', 'image/png')?.kind).toBe('image')
    expect(classifyAttachment('a.jpg', 'image/jpeg')?.kind).toBe('image')
    expect(classifyAttachment('a.jpeg', 'image/jpeg')?.kind).toBe('image')
  })

  it('normalises the image/jpg alias to image/jpeg', () => {
    expect(classifyAttachment('a.jpg', 'image/jpg')?.mime).toBe('image/jpeg')
  })

  it('falls back to the extension when the platform reports no MIME type', () => {
    expect(classifyAttachment('screenshot.PNG', '')).toEqual({ kind: 'image', mime: 'image/png' })
    expect(classifyAttachment('notes.md', '')).toEqual({ kind: 'text', mime: 'text/plain' })
  })

  it('accepts every whitelisted text extension', () => {
    for (const ext of ['txt', 'md', 'log', 'json', 'yaml', 'csv']) {
      expect(classifyAttachment(`file.${ext}`, '')?.kind).toBe('text')
    }
  })

  it('rejects image formats outside the whitelist even with an allowed extension', () => {
    expect(classifyAttachment('anim.gif', 'image/gif')).toBeNull()
    expect(classifyAttachment('pic.webp', 'image/webp')).toBeNull()
  })

  it('rejects unknown extensions and extension-less names', () => {
    expect(classifyAttachment('archive.zip', 'application/zip')).toBeNull()
    expect(classifyAttachment('Makefile', '')).toBeNull()
    expect(classifyAttachment('trailing.', '')).toBeNull()
  })
})

describe('validateAttachment', () => {
  it('accepts an image exactly at the size limit', () => {
    const result = validateAttachment(
      { name: 'at-limit.png', size: ATTACHMENT_LIMITS.maxImageBytes, type: 'image/png' },
      [],
    )
    expect(result.ok).toBe(true)
  })

  it('rejects an image one byte over the limit and reports the limit', () => {
    const result = validateAttachment(
      { name: 'big.png', size: ATTACHMENT_LIMITS.maxImageBytes + 1, type: 'image/png' },
      [],
    )
    expect(result.ok).toBe(false)
    if (!result.ok) {
      expect(result.error.key).toBe('attachmentTooLarge')
      expect(result.error.params?.name).toBe('big.png')
      expect(result.error.params?.limit).toBe('5.00 MB')
    }
  })

  it('accepts a text file exactly at the size limit and rejects one byte over', () => {
    const atLimit = validateAttachment(
      { name: 'a.log', size: ATTACHMENT_LIMITS.maxTextBytes, type: 'text/plain' },
      [],
    )
    expect(atLimit.ok).toBe(true)

    const overLimit = validateAttachment(
      { name: 'a.log', size: ATTACHMENT_LIMITS.maxTextBytes + 1, type: 'text/plain' },
      [],
    )
    expect(overLimit.ok).toBe(false)
    if (!overLimit.ok) expect(overLimit.error.key).toBe('attachmentTextTooLarge')
  })

  it('rejects zero-byte files', () => {
    const result = validateAttachment({ name: 'empty.log', size: 0, type: 'text/plain' }, [])
    expect(result.ok).toBe(false)
    if (!result.ok) expect(result.error.key).toBe('attachmentEmpty')
  })

  it('rejects an unsupported type and lists the supported formats', () => {
    const result = validateAttachment({ name: 'x.zip', size: 10, type: 'application/zip' }, [])
    expect(result.ok).toBe(false)
    if (!result.ok) {
      expect(result.error.key).toBe('attachmentUnsupportedType')
      expect(String(result.error.params?.formats)).toContain('png')
      expect(String(result.error.params?.formats)).toContain('log')
    }
  })

  it('allows images up to the per-message limit', () => {
    const three = [image(), image(), image()]
    expect(three).toHaveLength(ATTACHMENT_LIMITS.maxImagesPerMessage - 1)
    expect(validateAttachment({ name: 'fourth.png', size: 1024, type: 'image/png' }, three).ok).toBe(true)

    const full = [...three, image()]
    expect(full).toHaveLength(ATTACHMENT_LIMITS.maxImagesPerMessage)
    const result = validateAttachment({ name: 'fifth.png', size: 1024, type: 'image/png' }, full)
    expect(result.ok).toBe(false)
    if (!result.ok) {
      expect(result.error.key).toBe('attachmentTooManyImages')
      expect(result.error.params?.limit).toBe(ATTACHMENT_LIMITS.maxImagesPerMessage)
    }
  })

  it('caps text files at the per-message limit but still allows an image', () => {
    const full = [textFile('a.log'), textFile('b.log'), textFile('c.log')]
    expect(full).toHaveLength(ATTACHMENT_LIMITS.maxTextFilesPerMessage)

    const rejected = validateAttachment({ name: 'd.log', size: 10, type: 'text/plain' }, full)
    expect(rejected.ok).toBe(false)
    if (!rejected.ok) expect(rejected.error.key).toBe('attachmentTooManyText')

    const stillAllowed = validateAttachment({ name: 'shot.png', size: 1024, type: 'image/png' }, full)
    expect(stillAllowed.ok).toBe(true)
  })
})

describe('attachmentToAnthropicBlock', () => {
  it('renders an image as a base64 source block', () => {
    expect(attachmentToAnthropicBlock(image())).toEqual({
      type: 'image',
      source: { type: 'base64', media_type: 'image/png', data: PNG_BASE64 },
    })
  })

  it('renders a text file as a plain text block, never base64', () => {
    const block = attachmentToAnthropicBlock(textFile('app.log', 'stack trace here'))
    expect(block.type).toBe('text')
    expect(block.text).toContain('stack trace here')
    expect(block.text).toContain('app.log')
    // A base64 encoding of the body must not appear anywhere in the block.
    expect(block.text).not.toContain(btoa('stack trace here'))
    expect(block.source).toBeUndefined()
  })

  it('delimits the text file so it cannot be mistaken for the user prompt', () => {
    const formatted = formatTextAttachment(textFile('a.log', 'line'))
    expect(formatted.startsWith('[Attached file: a.log')).toBe(true)
    expect(formatted).toContain('----- begin a.log -----')
    expect(formatted).toContain('----- end a.log -----')
  })
})

describe('buildUserContent', () => {
  it('returns the plain string when there are no attachments (wire compatibility)', () => {
    expect(buildUserContent('hello', [])).toBe('hello')
    expect(buildUserContent('hello', undefined)).toBe('hello')
  })

  it('orders blocks as text, images, then text files', () => {
    const blocks = buildUserContent('why?', [textFile(), image()]) as Array<Record<string, unknown>>
    expect(blocks.map((b) => b.type)).toEqual(['text', 'image', 'text'])
  })

  it('omits the leading text block when the user sent no prose', () => {
    const blocks = buildUserContent('', [image()]) as Array<Record<string, unknown>>
    expect(blocks).toHaveLength(1)
    expect(blocks[0].type).toBe('image')
  })
})

describe('dataUrlPayload', () => {
  it('splits a data URL into MIME and base64 payload', () => {
    expect(dataUrlPayload(`data:image/png;base64,${PNG_BASE64}`)).toEqual({ mime: 'image/png', data: PNG_BASE64 })
  })

  it('returns null for anything that is not a base64 data URL', () => {
    expect(dataUrlPayload('https://example.com/a.png')).toBeNull()
    expect(dataUrlPayload('data:image/png,notbase64')).toBeNull()
  })
})

describe('helpers', () => {
  it('exposes a file-picker accept list derived from the whitelist', () => {
    expect(ATTACHMENT_FILE_ACCEPT).toContain('.png')
    expect(ATTACHMENT_FILE_ACCEPT).toContain('.log')
    expect(ATTACHMENT_FILE_ACCEPT).toContain('image/jpeg')
  })

  it('formats byte sizes for the chips', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2.0 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.00 MB')
  })

  it('extracts a lower-cased extension', () => {
    expect(fileExtension('A.LOG')).toBe('log')
    expect(fileExtension('noext')).toBe('')
    expect(fileExtension('.hidden')).toBe('')
  })
})

describe('createImageAttachmentFromDataURL', () => {
  it('builds an attachment from a valid data URL', async () => {
    const att = await createImageAttachmentFromDataURL(`data:image/png;base64,${PNG_BASE64}`, 'shot.png', 70)
    expect(att.kind).toBe('image')
    expect(att.mime).toBe('image/png')
    expect(att.name).toBe('shot.png')
    expect(att.size).toBe(70)
    expect(att.data).toBe(PNG_BASE64)
  })

  it('rejects a non-data-URL payload', async () => {
    await expect(createImageAttachmentFromDataURL('https://example.com/a.png', 'a.png', 10)).rejects.toThrow(
      'empty image payload',
    )
  })

  it('passes the pixel cap through in environments with an image decoder (stubbed Image)', async () => {
    // Node has no Image; swap in a controllable one for this test only.
    const origImage = (globalThis as any).Image
    try {
      ;(globalThis as any).Image = class {
        naturalWidth = ATTACHMENT_LIMITS.maxImageSide + 1
        naturalHeight = 100
        onload: () => void = () => {}
        onerror: () => void = () => {}
        set src(_v: string) {
          // decode synchronously
          this.onload()
        }
      }
      await expect(
        createImageAttachmentFromDataURL(`data:image/png;base64,${PNG_BASE64}`, 'huge.png', 70),
      ).rejects.toThrow('image pixels too large')
    } finally {
      ;(globalThis as any).Image = origImage
    }
  })

  it('without an image decoder the backend pre-flight stays the authority', async () => {
    // In the plain Node test environment Image is undefined: dimensions come
    // back unknown and the attachment is created; the Go-side header parse
    // is the real gate (see TestValidateRequestAttachments).
    if (typeof Image !== 'undefined') return
    const att = await createImageAttachmentFromDataURL(`data:image/png;base64,${PNG_BASE64}`, 'x.png', 70)
    expect(att.kind).toBe('image')
  })
})

describe('redactAttachmentsForLog', () => {
  it('replaces large payload strings so base64 never reaches a log', () => {
    const redacted = redactAttachmentsForLog({
      content: [{ type: 'image', source: { data: 'A'.repeat(4096) } }],
      keep: 'short',
    }) as any
    expect(redacted.content[0].source.data).toBe('[redacted 4096 chars]')
    expect(redacted.keep).toBe('short')
  })

  it('leaves short strings untouched', () => {
    expect(redactAttachmentsForLog({ data: 'abc' })).toEqual({ data: 'abc' })
  })
})
