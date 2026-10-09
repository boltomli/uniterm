import type { AIAttachment, AIAttachmentKind } from '../types/ai'

/**
 * Image / file attachments for the AI assistant.
 *
 * uniTerm is a model proxy: it does NOT run OCR, image recognition or any other
 * multimodal inference locally. Attachments are validated here and forwarded to
 * the upstream multimodal model, which does the actual understanding.
 *
 * Every limit and whitelist lives in this file — nothing about attachments is
 * hard-coded anywhere else in the frontend. The backend mirrors the image
 * limits in `app_ai_attachment.go` as an un-bypassable guard rail; keep the two
 * in sync when either changes.
 */
export const ATTACHMENT_LIMITS = {
  /** Max bytes for a single image attachment (5 MiB). */
  maxImageBytes: 5 * 1024 * 1024,
  /** Max text-file bytes for a single attachment (5 MiB). */
  maxTextBytes: 5 * 1024 * 1024,
  /** Max pixels on the longest edge of an image (Anthropic rejects
   *  anything above 8000 x 8000; other providers are similar or tighter). */
  maxImageSide: 8000,
  /** Max images per message. 4 keeps a screenshot-comparison turn
   * (before / after, two logs side by side) working while staying under
   * ChatGPT's ~10 and Claude.ai's effectively-unlimited-but-slow; the
   * backend hard ceiling above this (8) absorbs messages merged from
   * consecutive turns. */
  maxImagesPerMessage: 4,
  /** Max text files per message. */
  maxTextFilesPerMessage: 3,
} as const

/** Image MIME types accepted from the clipboard and the file picker.
 *  png/jpeg/gif/webp are exactly the formats the major providers accept as
 *  image blocks. */
export const ATTACHMENT_IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'] as const
/** Image extensions accepted from the file picker (used when MIME is absent). */
export const ATTACHMENT_IMAGE_EXTENSIONS = ['png', 'jpg', 'jpeg', 'gif', 'webp'] as const
/** Text-file extensions accepted from the file picker. Code, config and data
 *  formats that are plain text — the model reads them verbatim. */
export const ATTACHMENT_TEXT_EXTENSIONS = [
  // documents / data
  'txt', 'md', 'log', 'json', 'yaml', 'yml', 'csv', 'rst', 'adoc', 'tex', 'diff', 'patch',
  // code
  'go', 'py', 'js', 'mjs', 'cjs', 'ts', 'tsx', 'jsx', 'java', 'c', 'h', 'cpp', 'hpp', 'cc',
  'rs', 'rb', 'php', 'kt', 'swift', 'cs', 'sh', 'bat', 'ps1', 'sql', 'lua', 'vue',
  // config
  'xml', 'toml', 'ini', 'cfg', 'conf', 'properties',
] as const
/** Well-known extension-less file names attached as text. Extension-based
 *  classification cannot see these (Makefile has no dot, .gitignore's only
 *  dot is the hidden-file prefix), so they are matched by lowercased name. */
export const ATTACHMENT_TEXT_FILENAMES = [
  'makefile', 'dockerfile', 'readme', 'license', 'changelog',
  '.gitignore', '.editorconfig', '.env',
] as const

/** `accept` attribute for the hidden file input, derived from the lists above.
 *  Both MIME and extension forms are listed so every platform's picker filters
 *  correctly; the picker is only a hint — validateAttachment is the gate. */
export const ATTACHMENT_FILE_ACCEPT = [
  ...ATTACHMENT_IMAGE_TYPES,
  ...ATTACHMENT_IMAGE_EXTENSIONS.map((ext) => `.${ext}`),
  ...ATTACHMENT_TEXT_EXTENSIONS.map((ext) => `.${ext}`),
].join(',')

/** Why an attachment was rejected. Mapped to `ai.<key>` by the UI layer. */
export type AttachmentErrorKey =
  | 'attachmentUnsupportedType'
  | 'attachmentTooLarge'
  | 'attachmentTooLargePixels'
  | 'attachmentTextTooLarge'
  | 'attachmentTooManyImages'
  | 'attachmentTooManyText'
  | 'attachmentEmpty'
  | 'attachmentBinary'
  | 'attachmentReadFailed'

export interface AttachmentError {
  key: AttachmentErrorKey
  params?: Record<string, string | number>
}

export type ValidateResult =
  | { ok: true; kind: AIAttachmentKind; mime: string }
  | { ok: false; error: AttachmentError }

export function attachmentErrorMessageKey(error: AttachmentError): string {
  return `ai.${error.key}`
}

/** Lower-cased extension without the dot, or '' when there is none. */
export function fileExtension(name: string): string {
  const idx = name.lastIndexOf('.')
  if (idx <= 0 || idx === name.length - 1) return ''
  return name.slice(idx + 1).toLowerCase()
}

/** Whitelist label used in the "unsupported type" message. */
export const attachmentFormatsLabel = [...ATTACHMENT_IMAGE_EXTENSIONS, ...ATTACHMENT_TEXT_EXTENSIONS].join(' / ')

/** Decode a base64 payload (no data-URL prefix) into raw bytes. Used to wrap
 *  natively-dropped files — read off disk by the backend binding — back into
 *  a File so the regular validation pipeline can run on them. */
export function bytesFromBase64(b64: string): Uint8Array<ArrayBuffer> {
  const bin = atob(b64)
  const buf = new ArrayBuffer(bin.length)
  const bytes = new Uint8Array(buf)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  return bytes
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return ''
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

/**
 * Classify a candidate attachment. Returns null when the type is outside the
 * whitelist — the caller turns that into the "supported formats" message.
 *
 * MIME is authoritative when the platform provides it (clipboard blobs always
 * do); the extension is the fallback for file pickers that report "".
 */
export function classifyAttachment(name: string, mime: string): { kind: AIAttachmentKind; mime: string } | null {
  const normalizedMime = (mime || '').toLowerCase()
  const ext = fileExtension(name)

  if ((ATTACHMENT_IMAGE_TYPES as readonly string[]).includes(normalizedMime)) {
    return { kind: 'image', mime: normalizedMime }
  }
  // Some platforms report image/jpg; normalise it to image/jpeg.
  if (normalizedMime === 'image/jpg') {
    return { kind: 'image', mime: 'image/jpeg' }
  }
  if (normalizedMime.startsWith('image/')) {
    // A real image type we do not support (gif/webp/bmp/svg...): reject by
    // MIME rather than silently accepting it via a misleading extension.
    return null
  }
  if ((ATTACHMENT_IMAGE_EXTENSIONS as readonly string[]).includes(ext)) {
    return {
      kind: 'image',
      mime: ext === 'png' ? 'image/png'
        : ext === 'gif' ? 'image/gif'
        : ext === 'webp' ? 'image/webp'
        : 'image/jpeg',
    }
  }
  if ((ATTACHMENT_TEXT_EXTENSIONS as readonly string[]).includes(ext)) {
    return { kind: 'text', mime: normalizedMime || 'text/plain' }
  }
  if ((ATTACHMENT_TEXT_FILENAMES as readonly string[]).includes(name.toLowerCase())) {
    return { kind: 'text', mime: normalizedMime || 'text/plain' }
  }
  return null
}

/**
 * Validate a candidate against the whitelist, the per-file size cap and the
 * per-message count caps. Never silently drops: every rejection carries a
 * reason the UI renders as a readable message.
 */
export function validateAttachment(
  file: { name: string; size: number; type: string },
  existing: readonly AIAttachment[],
): ValidateResult {
  const classified = classifyAttachment(file.name, file.type)
  if (!classified) {
    return {
      ok: false,
      error: {
        key: 'attachmentUnsupportedType',
        params: {
          name: file.name,
          formats: attachmentFormatsLabel,
        },
      },
    }
  }

  if (file.size <= 0) {
    return { ok: false, error: { key: 'attachmentEmpty', params: { name: file.name } } }
  }

  if (classified.kind === 'image') {
    if (file.size > ATTACHMENT_LIMITS.maxImageBytes) {
      return {
        ok: false,
        error: {
          key: 'attachmentTooLarge',
          params: { name: file.name, limit: formatBytes(ATTACHMENT_LIMITS.maxImageBytes) },
        },
      }
    }
    const images = existing.filter((a) => a.kind === 'image').length
    if (images >= ATTACHMENT_LIMITS.maxImagesPerMessage) {
      return {
        ok: false,
        error: { key: 'attachmentTooManyImages', params: { limit: ATTACHMENT_LIMITS.maxImagesPerMessage } },
      }
    }
    return { ok: true, kind: 'image', mime: classified.mime }
  }

  if (file.size > ATTACHMENT_LIMITS.maxTextBytes) {
    return {
      ok: false,
      error: {
        key: 'attachmentTextTooLarge',
        params: { name: file.name, limit: formatBytes(ATTACHMENT_LIMITS.maxTextBytes) },
      },
    }
  }
  const textFiles = existing.filter((a) => a.kind === 'text').length
  if (textFiles >= ATTACHMENT_LIMITS.maxTextFilesPerMessage) {
    return {
      ok: false,
      error: { key: 'attachmentTooManyText', params: { limit: ATTACHMENT_LIMITS.maxTextFilesPerMessage } },
    }
  }
  return { ok: true, kind: 'text', mime: classified.mime }
}

let attachmentSeq = 0

function nextAttachmentId(): string {
  attachmentSeq += 1
  return `att-${Date.now()}-${attachmentSeq}`
}

/** Strip the `data:<mime>;base64,` prefix from a data URL. */
export function dataUrlPayload(dataUrl: string): { mime: string; data: string } | null {
  const match = /^data:([^;,]+);base64,(.*)$/s.exec(dataUrl)
  if (!match) return null
  return { mime: match[1].toLowerCase(), data: match[2] }
}

/**
 * Turn an already-validated image payload into an attachment. `data` is raw
 * base64 (no data-URL prefix) — that is the shape every protocol adapter wants.
 */
export function createImageAttachment(name: string, mime: string, data: string, size: number): AIAttachment {
  return {
    id: nextAttachmentId(),
    kind: 'image',
    name,
    mime,
    size,
    data,
  }
}

/**
 * Turn a text file's contents into an attachment. Text is stored verbatim —
 * it is never base64-encoded so the model reads it as plain text and the
 * session file stays inspectable.
 */
export function createTextAttachment(name: string, mime: string, content: string, size: number): AIAttachment {
  return {
    id: nextAttachmentId(),
    kind: 'text',
    name,
    mime,
    size,
    data: content,
  }
}

/**
 * Read a File as base64 (images) or UTF-8 text (text files).
 *
 * Images additionally pass the pixel cap (see createImageAttachmentFromDataURL)
 * — the byte check in validateAttachment cannot see frame dimensions.
 */
export function readAttachmentFile(file: File, kind: AIAttachmentKind, mime: string): Promise<AIAttachment> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('read failed'))
    reader.onload = () => {
      if (kind === 'image') {
        const raw = typeof reader.result === 'string' ? reader.result : ''
        createImageAttachmentFromDataURL(raw, file.name, file.size, mime).then(resolve, reject)
        return
      }
      const text = typeof reader.result === 'string' ? reader.result : ''
      if (text.length === 0) {
        reject(new Error('empty file'))
        return
      }
      // A NUL byte means the file is binary with a text-ish extension; sending
      // it as text would corrupt the prompt (and the session file).
      if (text.includes('\u0000')) {
        reject(new Error('binary file'))
        return
      }
      resolve(createTextAttachment(file.name, mime, text, file.size))
    }
    if (kind === 'image') {
      reader.readAsDataURL(file)
    } else {
      reader.readAsText(file)
    }
  })
}

/**
 * Decode the pixel dimensions of an image data URL. Rejects when the browser
 * cannot decode the payload at all (a corrupt "image" the backend would
 * reject anyway — failing here gives the user a readable message instead).
 * Environments without an image decoder (tests) resolve unknown dimensions;
 * the backend's own header parse remains the authority there.
 */
export function imageDimensions(dataUrl: string): Promise<{ width: number; height: number }> {
  return new Promise((resolve, reject) => {
    if (typeof Image === 'undefined') {
      resolve({ width: 0, height: 0 })
      return
    }
    const img = new Image()
    img.onload = () => resolve({ width: img.naturalWidth, height: img.naturalHeight })
    img.onerror = () => reject(new Error('image decode failed'))
    img.src = dataUrl
  })
}

/**
 * Turn a freshly-read image data URL into an attachment, enforcing the pixel
 * cap the byte check cannot see: a long screenshot is often tiny in bytes but
 * thousands of pixels tall, and providers reject oversized frames
 * independently of byte size. Rejects with 'image pixels too large' so the
 * caller can map it to its own message key.
 */
export function createImageAttachmentFromDataURL(
  dataUrl: string,
  name: string,
  size: number,
  fallbackMime = '',
): Promise<AIAttachment> {
  const payload = dataUrlPayload(dataUrl)
  if (!payload || !payload.data) return Promise.reject(new Error('empty image payload'))
  return imageDimensions(dataUrl).then((dims) => {
    if (dims.width > ATTACHMENT_LIMITS.maxImageSide || dims.height > ATTACHMENT_LIMITS.maxImageSide) {
      throw new Error('image pixels too large')
    }
    return createImageAttachment(name, payload.mime || fallbackMime, payload.data, size)
  })
}

/** The `data:` URL used to render an attachment thumbnail in the UI. */
export function attachmentPreviewUrl(attachment: AIAttachment): string {
  return `data:${attachment.mime};base64,${attachment.data}`
}

/** Header/footer wrapped around an attached text file so it cannot be confused
 *  with the user's own prose. */
export function formatTextAttachment(attachment: AIAttachment): string {
  return `[Attached file: ${attachment.name} (${formatBytes(attachment.size)})]\n----- begin ${attachment.name} -----\n${attachment.data}\n----- end ${attachment.name} -----`
}

/** Anthropic-format content block for one attachment. */
export function attachmentToAnthropicBlock(attachment: AIAttachment): Record<string, unknown> {
  if (attachment.kind === 'image') {
    return {
      type: 'image',
      source: {
        type: 'base64',
        media_type: attachment.mime,
        data: attachment.data,
      },
    }
  }
  return { type: 'text', text: formatTextAttachment(attachment) }
}

/**
 * Build the user message content for the API request.
 *
 * Returns a plain string when there are no attachments so the request body is
 * byte-for-byte what it was before this feature existed; otherwise returns an
 * ordered block array (text, then images, then text files).
 */
export function buildUserContent(
  text: string,
  attachments: readonly AIAttachment[] | undefined,
): string | Array<Record<string, unknown>> {
  if (!attachments || attachments.length === 0) return text
  const blocks: Array<Record<string, unknown>> = []
  if (text) blocks.push({ type: 'text', text })
  for (const attachment of attachments) {
    if (attachment.kind === 'image') blocks.push(attachmentToAnthropicBlock(attachment))
  }
  for (const attachment of attachments) {
    if (attachment.kind === 'text') blocks.push(attachmentToAnthropicBlock(attachment))
  }
  return blocks
}

/**
 * Redact attachment payloads before a request is serialized for the debug
 * panel / logs. Image bytes must never be written to a log or a debug dump.
 */
export function redactAttachmentsForLog(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(redactAttachmentsForLog)
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {}
    for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
      if (key === 'data' && typeof entry === 'string' && entry.length > 512) {
        out[key] = `[redacted ${entry.length} chars]`
      } else {
        out[key] = redactAttachmentsForLog(entry)
      }
    }
    return out
  }
  return value
}
