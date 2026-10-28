<template>
  <div
    ref="editor" class="desktop-region-editor" role="dialog" aria-modal="true" :aria-label="t('remoteDesktop.customRegion')"
    tabindex="-1" @keydown.stop @keyup.stop @keydown.esc.prevent="emit('cancel')" @keydown.ctrl.enter.prevent="save" @keydown.tab.prevent="cycleFocus"
    @pointerdown.stop @pointermove.stop @pointerup.stop @wheel.prevent.stop @contextmenu.prevent
  >
    <svg
      ref="previewSvg" class="region-preview" :viewBox="`0 0 ${preview.width} ${preview.height}`"
      :aria-label="t('remoteDesktop.regionDragHint')"
      @pointerdown="beginRegionDrag($event, 'draw')" @pointermove="moveRegion" @pointerup="endRegionDrag"
      @pointercancel="endRegionDrag" @lostpointercapture="endRegionDrag"
    >
      <defs>
        <mask :id="maskId">
          <rect width="100%" height="100%" fill="white" />
          <rect :x="draft.x" :y="draft.y" :width="draft.width" :height="draft.height" fill="black" />
        </mask>
      </defs>
      <image :href="preview.dataUrl" :width="preview.width" :height="preview.height" />
      <rect width="100%" height="100%" fill="black" fill-opacity="0.55" :mask="`url(#${maskId})`" />
      <rect
        v-for="region in otherRegions" :key="region.id" :x="region.x" :y="region.y" :width="region.width" :height="region.height"
        fill="none" stroke="white" stroke-opacity="0.45" stroke-dasharray="5 5" vector-effect="non-scaling-stroke" pointer-events="none"
      />
      <rect
        class="region-selection" :x="draft.x" :y="draft.y" :width="draft.width" :height="draft.height"
        fill="transparent" stroke="#60a5fa" stroke-width="2" vector-effect="non-scaling-stroke"
        @pointerdown.stop="beginRegionDrag($event, 'move')"
      />
      <rect
        v-for="handle in handles" :key="handle.mode" :x="handle.x - handleSize / 2" :y="handle.y - handleSize / 2"
        :width="handleSize" :height="handleSize" fill="white" stroke="#2563eb" vector-effect="non-scaling-stroke"
        :style="{ cursor: `${handle.mode}-resize` }" @pointerdown.stop="beginRegionDrag($event, handle.mode)"
      />
    </svg>

    <div
      ref="toolbar" class="region-editor-toolbar" :style="{ left: `${toolbarPosition.x}px`, top: `${toolbarPosition.y}px` }"
      @pointerdown.stop @wheel.stop
    >
      <div class="region-toolbar-heading">
        <span class="region-toolbar-grip" @pointerdown.prevent="beginToolbarDrag" @pointermove="moveToolbar" @pointerup="endToolbarDrag" @pointercancel="endToolbarDrag" @lostpointercapture="endToolbarDrag">
          <GripVertical :size="18" />
        </span>
        <el-input v-model="draft.name" class="region-name" size="small" maxlength="80" :placeholder="t('remoteDesktop.regionName')" :aria-label="t('remoteDesktop.regionName')" />
        <el-button v-if="editingExisting" size="small" type="danger" plain @click="emit('delete', draft.id)">{{ t('common.delete') }}</el-button>
        <el-button size="small" @click="emit('cancel')">{{ t('common.cancel') }}</el-button>
        <el-button size="small" type="primary" :disabled="!valid" @click="save">{{ t('common.save') }}</el-button>
      </div>
      <div class="region-toolbar-coordinates">
        <label v-for="field in fields" :key="field.key">
          <span>{{ field.label }}</span>
          <el-input-number
            :model-value="draft[field.key]" size="small" controls-position="right" :precision="0"
            :min="field.key === 'x' || field.key === 'y' ? 0 : 1" :max="coordinateMax(field.key)"
            :aria-label="field.description" @input="(value: number | null | undefined) => changeNumber(field.key, value)" @update:model-value="(value: number | null | undefined) => changeNumber(field.key, value)"
          />
        </label>
      </div>
      <p v-if="!sameSize" class="region-toolbar-error">{{ t('remoteDesktop.regionDesktopChanged') }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { GripVertical } from '@lucide/vue'
import { useI18n } from '../i18n'
import { changeDesktopCoordinate, dragDesktopRegion, fitDesktopRect, type RegionDrag, type RegionDragMode } from '../utils/desktopRegions'
import { validDesktopRect, type DesktopPreview, type DesktopRect, type DesktopRegion } from '../utils/vncDisplays'

const props = defineProps<{
  preview: DesktopPreview
  region: DesktopRegion
  regions: DesktopRegion[]
  editingExisting: boolean
  desktopWidth: number
  desktopHeight: number
}>()
const emit = defineEmits<{
  save: [region: DesktopRegion]
  cancel: []
  delete: [id: string]
}>()
const { t } = useI18n()
const editor = ref<HTMLDivElement | null>(null)
const previewSvg = ref<SVGSVGElement | null>(null)
const toolbar = ref<HTMLDivElement | null>(null)
const draft = reactive({ ...props.region, ...fitDesktopRect(props.region, props.preview.width, props.preview.height) })
const toolbarPosition = reactive({ x: 12, y: 12 })
const maskId = `desktop-region-${Math.random().toString(36).slice(2)}`
const sameSize = computed(() => props.preview.width === props.desktopWidth && props.preview.height === props.desktopHeight)
const valid = computed(() => !!draft.name.trim() && sameSize.value && validDesktopRect(draft, props.desktopWidth, props.desktopHeight))
const otherRegions = computed(() => props.regions.filter(region => region.id !== draft.id && validDesktopRect(region, props.preview.width, props.preview.height)))
const fields = computed(() => [
  { key: 'x' as const, label: 'X', description: 'X' }, { key: 'y' as const, label: 'Y', description: 'Y' },
  { key: 'height' as const, label: 'H', description: t('remoteDesktop.regionHeight') },
  { key: 'width' as const, label: 'W', description: t('remoteDesktop.regionWidth') },
])
const previewSize = reactive({ width: 1280, height: 800 })
const handleSize = computed(() => 10 / Math.max(0.0001, Math.min(previewSize.width / props.preview.width, previewSize.height / props.preview.height)))
const handles = computed(() => {
  const x = draft.x, y = draft.y, right = x + draft.width, bottom = y + draft.height
  return [
    { mode: 'nw' as const, x, y }, { mode: 'n' as const, x: (x + right) / 2, y }, { mode: 'ne' as const, x: right, y },
    { mode: 'w' as const, x, y: (y + bottom) / 2 }, { mode: 'e' as const, x: right, y: (y + bottom) / 2 },
    { mode: 'sw' as const, x, y: bottom }, { mode: 's' as const, x: (x + right) / 2, y: bottom }, { mode: 'se' as const, x: right, y: bottom },
  ]
})
let drag: RegionDrag | null = null
let regionPointer: number | null = null
let toolbarDrag: { x: number; y: number; left: number; top: number; id: number } | null = null
let resizeObserver: ResizeObserver | null = null

function point(event: PointerEvent) {
  const matrix = previewSvg.value?.getScreenCTM()
  if (!matrix) return null
  const p = new DOMPoint(event.clientX, event.clientY).matrixTransform(matrix.inverse())
  return { x: Math.min(props.preview.width, Math.max(0, Math.round(p.x))), y: Math.min(props.preview.height, Math.max(0, Math.round(p.y))) }
}

function beginRegionDrag(event: PointerEvent, mode: RegionDragMode) {
  if (event.button !== 0 || regionPointer !== null) return
  const start = point(event)
  if (!start) return
  event.preventDefault()
  // Commit an in-progress numeric edit before pointer changes update its model.
  editor.value?.focus({ preventScroll: true })
  regionPointer = event.pointerId
  drag = { mode, start, rect: { x: draft.x, y: draft.y, width: draft.width, height: draft.height } }
  previewSvg.value?.setPointerCapture(event.pointerId)
  if (mode === 'draw') Object.assign(draft, dragDesktopRegion(drag, start, props.preview.width, props.preview.height))
}

function moveRegion(event: PointerEvent) {
  if (!drag || regionPointer !== event.pointerId) return
  const current = point(event)
  if (current) Object.assign(draft, dragDesktopRegion(drag, current, props.preview.width, props.preview.height))
}

function endRegionDrag(event: PointerEvent) {
  if (regionPointer !== event.pointerId) return
  if (event.type === 'pointerup') moveRegion(event)
  drag = null
  regionPointer = null
  if (previewSvg.value?.hasPointerCapture(event.pointerId)) previewSvg.value.releasePointerCapture(event.pointerId)
}

function coordinateMax(key: keyof DesktopRect) {
  return { x: props.preview.width - draft.width, y: props.preview.height - draft.height,
    width: props.preview.width - draft.x, height: props.preview.height - draft.y }[key]
}

function changeNumber(key: keyof DesktopRect, value: number | null | undefined) {
  if (typeof value === 'number' && Number.isFinite(value)) Object.assign(draft, changeDesktopCoordinate(draft, key, value, props.preview.width, props.preview.height))
}


function clampToolbar() {
  if (!editor.value || !toolbar.value) return
  toolbarPosition.x = Math.max(0, Math.min(toolbarPosition.x, editor.value.clientWidth - toolbar.value.offsetWidth))
  toolbarPosition.y = Math.max(0, Math.min(toolbarPosition.y, editor.value.clientHeight - toolbar.value.offsetHeight))
  previewSize.width = editor.value.clientWidth
  previewSize.height = editor.value.clientHeight
}

function beginToolbarDrag(event: PointerEvent) {
  if (event.button !== 0 || toolbarDrag) return
  editor.value?.focus({ preventScroll: true })
  toolbarDrag = { x: event.clientX, y: event.clientY, left: toolbarPosition.x, top: toolbarPosition.y, id: event.pointerId }
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
}

function moveToolbar(event: PointerEvent) {
  if (!toolbarDrag || toolbarDrag.id !== event.pointerId) return
  toolbarPosition.x = toolbarDrag.left + event.clientX - toolbarDrag.x
  toolbarPosition.y = toolbarDrag.top + event.clientY - toolbarDrag.y
  clampToolbar()
}

function endToolbarDrag(event: PointerEvent) {
  if (toolbarDrag?.id !== event.pointerId) return
  toolbarDrag = null
  const target = event.currentTarget as HTMLElement
  if (target.hasPointerCapture(event.pointerId)) target.releasePointerCapture(event.pointerId)
}

function save() {
  if (valid.value) emit('save', { ...draft, name: draft.name.trim() })
}

function cycleFocus(event: KeyboardEvent) {
  const targets = Array.from(editor.value?.querySelectorAll<HTMLElement>('input:not(:disabled), select:not(:disabled), button:not(:disabled), [tabindex="0"]') ?? [])
  if (!targets.length) return
  const current = targets.indexOf(document.activeElement as HTMLElement)
  const next = current < 0 ? (event.shiftKey ? targets.length - 1 : 0) :
    (current + (event.shiftKey ? -1 : 1) + targets.length) % targets.length
  targets[next]?.focus()
}

onMounted(async () => {
  await nextTick()
  editor.value?.focus({ preventScroll: true })
  clampToolbar()
  resizeObserver = new ResizeObserver(clampToolbar)
  if (editor.value) resizeObserver.observe(editor.value)
  if (toolbar.value) resizeObserver.observe(toolbar.value)
})
onBeforeUnmount(() => resizeObserver?.disconnect())
</script>

<style scoped>
.desktop-region-editor { position: absolute; inset: 0; z-index: 100; background: var(--bg-base); color: var(--text-primary); outline: none; }
.region-preview { position: absolute; inset: 0; width: 100%; height: 100%; touch-action: none; cursor: crosshair; user-select: none; }
.region-selection { cursor: move; }
.region-editor-toolbar { position: absolute; max-width: calc(100% - 1.5rem); padding: 0.625rem; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--bg-elevated); box-shadow: var(--shadow-lg); font-size: 0.75rem; }
.region-toolbar-heading, .region-toolbar-coordinates { display: flex; flex-wrap: wrap; align-items: center; gap: 0.5rem; }
.region-toolbar-heading { margin-bottom: 0.625rem; }
.region-toolbar-grip { display: flex; align-items: center; padding: 0.25rem 0; cursor: move; touch-action: none; color: var(--text-muted); }
.region-name { width: 8rem; }
.region-toolbar-heading :deep(.el-button + .el-button) { margin-left: 0; }
.region-toolbar-coordinates label { display: flex; align-items: center; gap: 0.35rem; }
.region-toolbar-coordinates :deep(.el-input-number) { width: 4rem; }
.region-toolbar-coordinates :deep(.el-input-number.is-controls-right .el-input__wrapper) { padding-left: 0.25rem; padding-right: 1.25rem; }
.region-toolbar-coordinates :deep(.el-input-number.is-controls-right .el-input-number__increase), .region-toolbar-coordinates :deep(.el-input-number.is-controls-right .el-input-number__decrease) { width: 1rem; }
.region-toolbar-coordinates :deep(.el-input__inner) { font-variant-numeric: tabular-nums; }
.region-toolbar-error { margin: 0.35rem 0 0; color: var(--error); }
</style>
