<template>
  <el-select
    :model-value="modelValue"
    class="remote-desktop-zoom-select"
    :class="{ 'is-compact': compact }"
    :disabled="disabled"
    :teleported="teleported"
    :fit-input-width="false"
    :aria-label="t('remoteDesktop.scale')"
    @update:model-value="onChange"
  >
    <el-option v-if="allowFit" :value="'adaptive'" :label="t('remoteDesktop.zoomFit')" />
    <el-option v-if="allowFit && allowStretch" :value="'stretch'" :label="t('remoteDesktop.zoomStretch')" />
    <el-option v-for="z in ZOOM_FACTORS" :key="z" :value="z" :label="`${Math.round(z * 100)}%`" />
  </el-select>
</template>

<script setup lang="ts">
// Display zoom selector shared by the RDP/VNC/SPICE status bars and the
// fullscreen toolbar: 'adaptive' (fit pane) or a scale factor (100% default).
// The fit entry only makes sense when the framebuffer is NOT already resized
// to fill the pane — RDP adaptive resolution hides it.
// teleported=false keeps the dropdown inside the fullscreen element (a body
// portal is invisible while requestFullscreen is active).
import { useI18n } from '../i18n'
import { ZOOM_FACTORS } from '../utils/rdpZoom'
import type { RdpZoom } from '../utils/rdpZoom'

withDefaults(defineProps<{
  modelValue: RdpZoom
  disabled?: boolean
  allowFit?: boolean
  // noVNC maps pointers through a single uniform scale, so it hides the
  // stretch entry until per-axis mapping exists (issue #1310).
  allowStretch?: boolean
  compact?: boolean
  teleported?: boolean
}>(), { disabled: false, allowFit: true, allowStretch: true, compact: true, teleported: true })
const emit = defineEmits<{ 'update:modelValue': [value: RdpZoom] }>()
const { t } = useI18n()

function onChange(value: unknown) {
  emit('update:modelValue', value === 'adaptive' || value === 'stretch' || typeof value === 'number' ? value as RdpZoom : 1)
}
</script>

<style scoped>
/* Compact metrics shared with the other status bar selects. */
.remote-desktop-zoom-select.is-compact {
  width: 5.5rem;
  --el-select-width: 5.5rem;
}
.remote-desktop-zoom-select.is-compact :deep(.el-select__wrapper) {
  min-height: 1.125rem;
  height: 1.125rem;
  padding: 0 0.25rem;
  font-size: 0.6875rem;
}
</style>
