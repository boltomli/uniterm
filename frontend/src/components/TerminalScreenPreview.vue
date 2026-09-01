<template>
  <div
    v-show="visible"
    class="terminal-screen-preview"
    :style="popupStyle"
    @mouseenter="onPopupEnter"
    @mouseleave="hide"
    @click="onJump"
  >
    <div
      v-for="(row, i) in lines"
      :key="i"
      class="preview-line"
      :style="{ height: cellHeightPx, lineHeight: cellHeightPx }"
    >
      <span
        v-if="showTimestamps"
        class="preview-col-time"
        :style="{ width: timeColWidth }"
      >{{ row.timestamp }}&nbsp;</span>
      <span
        v-if="showLineNumbers"
        class="preview-col-num"
        :style="{ width: numColWidth }"
      >{{ row.lineNumber }}</span>
      <span class="preview-content">
        <template v-if="row.runs.length">
          <span
            v-for="(run, j) in row.runs"
            :key="j"
            :style="runStyle(run)"
          >{{ run.text }}</span>
        </template>
        <span v-else class="preview-blank">&nbsp;</span>
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
// Issue #729 — screen preview (屏幕回看): hovering the terminal scrollbar for
// a moment pops up a small read-only preview of the scrollback at that
// position (WindTerm/Termora-style). Clicking the preview scrolls the
// terminal there. Pure display: never touches the pty stream or terminal
// state, so TUI apps (vim, htop) are unaffected.
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import type { Terminal } from '@xterm/xterm'
import { getManagedTerminal } from '../services/terminalManager'
import { useSettingsStore } from '../stores/settingsStore'
import { formatTimestampMs, resolveRowTimestamp } from '../utils/terminalGutter'
import {
  buildPalette,
  lineToRuns,
  computePreviewStart,
  pickVerticalTrackIndex,
  type PreviewStyleRun,
} from '../services/screenPreview'

const props = defineProps<{ sessionId: string; enabled?: boolean }>()

const PREVIEW_ROWS = 10
const HOVER_DELAY = 250
const HIDE_DELAY = 150

const settingsStore = useSettingsStore()
const showLineNumbers = computed(() => settingsStore.settings.terminal.showLineNumbers ?? false)
const showTimestamps = computed(() => settingsStore.settings.terminal.showTimestamps ?? false)

const visible = ref(false)
const lines = ref<Array<{ runs: PreviewStyleRun[]; lineNumber: string; timestamp: string }>>([])
const startRow = ref(0)
const pos = ref({ left: 0, top: 0 })
const size = ref({ width: 0, height: 0 })
const cellHeightPx = ref('1.5em')
const bgColor = ref('#1e1e1e')
const fontCss = ref({ fontSize: '13px', fontFamily: 'monospace' })
const numColWidth = ref('')
const timeColWidth = ref('')

const popupStyle = computed(() => ({
  left: `${pos.value.left}px`,
  top: `${pos.value.top}px`,
  width: `${size.value.width}px`,
  height: `${size.value.height}px`,
  background: bgColor.value,
  fontSize: fontCss.value.fontSize,
  fontFamily: fontCss.value.fontFamily,
}))

function runStyle(run: PreviewStyleRun) {
  return {
    color: run.fg,
    background: run.bg,
    fontWeight: run.bold ? 600 : undefined,
    fontStyle: run.italic ? 'italic' : undefined,
    textDecoration: run.underline ? 'underline' : undefined,
    opacity: run.dim ? 0.6 : undefined,
  }
}

function getTerminal(): Terminal | null {
  return getManagedTerminal(props.sessionId)?.terminal ?? null
}

/**
 * Locate xterm's scrollbar element. xterm v6 renders a VS Code-style overlay
 * scrollbar (`.xterm-scrollable-element > .scrollbar`) that is a *sibling* of
 * `.xterm-viewport` — hovering it never produces events on the viewport, so
 * hover detection must hit-test against this element instead.
 */
function findScrollbar(t: Terminal): HTMLElement | null {
  const el = t.element
  if (!el) return null
  const nodes = el.querySelectorAll<HTMLElement>('.xterm-scrollable-element > .scrollbar')
  const rects = Array.from(nodes).map((n) => n.getBoundingClientRect())
  const idx = pickVerticalTrackIndex(rects)
  return idx >= 0 ? nodes[idx] : null
}

let host: HTMLElement | null = null
let hoverTimer: number | undefined
let hideTimer: number | undefined
let lastRatio = 0
let lastClientY = 0

function clearTimers() {
  if (hoverTimer !== undefined) {
    clearTimeout(hoverTimer)
    hoverTimer = undefined
  }
  if (hideTimer !== undefined) {
    clearTimeout(hideTimer)
    hideTimer = undefined
  }
}

function bind() {
  unbind()
  const t = getTerminal()
  const el = t?.element
  if (!el) return
  host = el
  el.addEventListener('mousemove', onMouseMove)
  el.addEventListener('mouseleave', onMouseLeave)
}

function unbind() {
  host?.removeEventListener('mousemove', onMouseMove)
  host?.removeEventListener('mouseleave', onMouseLeave)
  host = null
  clearTimers()
  visible.value = false
}

function cellHeight(t: Terminal): number {
  try {
    const h = (t as any)._core?._renderService?.dimensions?.css?.cell?.height
    if (h > 0) return h
  } catch { /* internal API — fall through */ }
  return (t.options.fontSize ?? 13) * 1.5
}

function charWidth(t: Terminal): number {
  try {
    const w = (t as any)._core?._renderService?.dimensions?.css?.character?.width
    if (w > 0) return w
  } catch { /* internal API — fall through */ }
  return (t.options.fontSize ?? 13) * 0.6
}

/**
 * The scrollbar track under the pointer, or null when not hovering it.
 * Geometry-based on purpose: mouse events over xterm v6's overlay scrollbar
 * target the underlying `xterm-screen` (the scrollbar never becomes the event
 * target), so only coordinate containment against the scrollbar's rect works.
 * A small tolerance on the left edge makes the narrow (14px) strip easier to
 * hit. Hovering the thumb (slider) returns null — the thumb marks the content
 * that is already on screen, so there is nothing to preview there.
 */
function scrollbarRect(t: Terminal, e: MouseEvent): DOMRect | null {
  const sb = findScrollbar(t)
  if (!sb) return null
  const r = sb.getBoundingClientRect()
  const TOLERANCE = 6
  if (
    e.clientX < r.left - TOLERANCE ||
    e.clientX > r.right + 2 ||
    e.clientY < r.top ||
    e.clientY > r.bottom
  ) {
    return null
  }
  const slider = sb.querySelector('.slider')
  if (slider) {
    const s = slider.getBoundingClientRect()
    const PAD = 4
    if (e.clientY >= s.top - PAD && e.clientY <= s.bottom + PAD) return null
  }
  return r
}

function onMouseMove(e: MouseEvent) {
  if (!props.enabled) return hide()
  const t = getTerminal()
  if (!t || !host) return hide()

  const rect = scrollbarRect(t, e)
  if (!rect) return hide()
  // Alt-screen apps (vim, htop) have no scrollback to preview.
  if (t.buffer.active.type === 'alternate') return hide()
  const total = t.buffer.active.length
  if (total <= t.rows) return hide()

  lastRatio = Math.min(1, Math.max(0, (e.clientY - rect.top) / rect.height))
  lastClientY = e.clientY
  clearTimers()
  if (visible.value) {
    show(t, rect)
  } else {
    hoverTimer = window.setTimeout(() => {
      hoverTimer = undefined
      const t2 = getTerminal()
      if (t2?.element) show(t2, rect)
    }, HOVER_DELAY)
  }
}

function show(t: Terminal, sbRect: DOMRect) {
  const total = t.buffer.active.length
  const start = computePreviewStart(lastRatio, total, t.rows, PREVIEW_ROWS)
  startRow.value = start

  const palette = buildPalette(t.options.theme)
  const buf = t.buffer.active
  const managed = getManagedTerminal(props.sessionId)
  const lineOffset = managed?.lineOffset ?? 0
  const withNumbers = showLineNumbers.value
  const withTimestamps = showTimestamps.value && managed
  const tsFormat = settingsStore.settings.terminal.timestampFormat || 'HH:mm:ss'

  const rendered: Array<{ runs: PreviewStyleRun[]; lineNumber: string; timestamp: string }> = []
  for (let i = 0; i < PREVIEW_ROWS; i++) {
    const bufferLine = start + i
    const line = buf.getLine(bufferLine)
    const isWrapped = line?.isWrapped ?? false
    const runs = line ? lineToRuns(line, t.cols, palette) : []
    // Mirror the gutter's semantics: wrapped continuation rows belong to the
    // line above, so they carry no number/timestamp of their own.
    const numbered = withNumbers && !isWrapped
    const stamped = withTimestamps && !isWrapped
    rendered.push({
      runs,
      lineNumber: numbered ? String(lineOffset + bufferLine + 1) : '',
      timestamp: stamped
        ? (() => {
            const ts = managed
              ? resolveRowTimestamp(
                  bufferLine,
                  lineOffset,
                  (y) => buf.getLine(y),
                  (abs) => managed.lineTimestamps.get(abs),
                )
              : undefined
            return ts ? formatTimestampMs(ts, tsFormat) : ''
          })()
        : '',
    })
  }
  lines.value = rendered
  bgColor.value = t.options.theme?.background || '#1e1e1e'
  fontCss.value = {
    fontSize: `${t.options.fontSize ?? 13}px`,
    fontFamily: typeof t.options.fontFamily === 'string' ? t.options.fontFamily : 'monospace',
  }
  const maxDigits = String(lineOffset + Math.max(total - 1, 0) + 1).length
  // Column widths in px from the terminal's measured character width — the
  // CSS `ch` unit measures the '0' glyph and drifts from the real cell size.
  const cw = charWidth(t)
  numColWidth.value = withNumbers ? `${maxDigits * cw}px` : ''
  timeColWidth.value = withTimestamps ? `${(tsFormat.length + 1) * cw}px` : ''
  // The number/timestamp columns live INSIDE the popup, so the popup must be
  // wider than the terminal by exactly those columns — otherwise the content
  // area shrinks and the right-hand side of each line gets clipped. The left
  // gutter area of the terminal provides this extra room.
  const colsExtra =
    (withNumbers ? maxDigits * cw : 0) +
    (withTimestamps ? (tsFormat.length + 1) * cw : 0)

  const lh = cellHeight(t)
  cellHeightPx.value = `${lh}px`
  // Anchor measurements to the terminal's own container — never to the popup
  // itself: while the popup is hidden (v-show display:none) its offsetParent
  // is null, which used to misplace the very first appearance to (0,0).
  const base = (host?.closest('.base-terminal') as HTMLElement | null)?.getBoundingClientRect()
  if (!base) return
  const termRect = t.element?.getBoundingClientRect()
  if (!termRect) return

  // box-sizing: content-box — `width` IS the content area, so text lines up
  // with the terminal regardless of padding. CHROME (padding 4px ×2 + border
  // 1px ×2) only matters for the outer footprint when positioning/clamping.
  const POPUP_H_CHROME = 10
  // Base the content width on the actual rendered text area (.xterm-screen),
  // NOT the `.xterm` root: the root's width also spans the scrollbar strip,
  // which left a blank margin at the right edge of every preview line.
  const screenEl = t.element?.querySelector('.xterm-screen') as HTMLElement | null
  const contentWidth = screenEl?.getBoundingClientRect().width ?? t.cols * cw
  // Overlay the terminal window exactly: flush with its left edge and ending
  // at the scrollbar. (The previous right-anchored layout left a visible gap
  // on the left whenever the popup's content was narrower than the window.)
  // Text is left-aligned, so lines start exactly where the terminal's do;
  // the columns make the previewed text line up with the real columns.
  const areaRect = (host?.closest('.terminal-area') as HTMLElement | null)?.getBoundingClientRect()
  const width = areaRect
    ? Math.max(contentWidth + colsExtra, sbRect.left - areaRect.left - POPUP_H_CHROME)
    : contentWidth + colsExtra
  const left = areaRect
    ? Math.max(0, areaRect.left - base.left)
    : Math.max(4, sbRect.left - width - POPUP_H_CHROME - 6 - base.left)
  const height = PREVIEW_ROWS * lh
  size.value = { width, height }
  pos.value = {
    left,
    top: Math.min(
      Math.max(4, lastClientY - lh * 2 - base.top),
      Math.max(4, base.height - height - 8),
    ),
  }
  visible.value = true
}

function onMouseLeave() {
  clearTimers()
  // Grace period so moving onto the popup itself doesn't close it.
  hideTimer = window.setTimeout(() => {
    hideTimer = undefined
    hide()
  }, HIDE_DELAY)
}

function onPopupEnter() {
  if (hideTimer !== undefined) {
    clearTimeout(hideTimer)
    hideTimer = undefined
  }
}

function hide() {
  clearTimers()
  visible.value = false
}

function onJump() {
  const t = getTerminal()
  if (t) t.scrollToLine(startRow.value)
  hide()
}

watch(
  () => props.sessionId,
  () => nextTick(bind),
  { immediate: false },
)

watch(
  () => props.enabled,
  (on) => {
    if (!on) hide()
  },
)

onMounted(() => nextTick(bind))
onBeforeUnmount(unbind)
</script>

<style scoped>
.terminal-screen-preview {
  position: absolute;
  z-index: 30;
  overflow: hidden;
  border: 1px solid var(--el-border-color, rgba(255, 255, 255, 0.2));
  border-radius: 6px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.35);
  padding: 2px 4px;
  box-sizing: content-box;
  pointer-events: auto;
  cursor: pointer;
}

.preview-line {
  display: flex;
  white-space: pre;
  overflow: hidden;
}

.preview-col-time {
  flex: none;
  opacity: 0.75;
}

.preview-col-num {
  flex: none;
  text-align: right;
  opacity: 0.55;
}

.preview-content {
  flex: 1 1 auto;
  overflow: hidden;
}

.preview-blank {
  display: inline-block;
}
</style>
