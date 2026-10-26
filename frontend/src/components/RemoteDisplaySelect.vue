<template>
  <div class="remote-display-select" @keydown.stop @keyup.stop>
    <span>{{ t('remoteDesktop.display') }}</span>
    <select :value="view.selected" :aria-label="t('remoteDesktop.display')" :disabled="unavailable" @change="choose">
      <option value="all">{{ t('remoteDesktop.allDisplays') }}</option>
      <option v-for="(display, i) in displays" :key="display.id" :value="`screen:${display.id}`">
        {{ t('remoteDesktop.displayNumber', { n: i + 1 }) }}{{ display.primary ? ` (${t('remoteDesktop.primaryDisplay')})` : '' }} · {{ display.width }} × {{ display.height }}
      </option>
      <optgroup v-if="allowCustom && regions.length" :label="t('remoteDesktop.customRegions')">
        <option v-for="(region, i) in regions" :key="region.id" :value="`region:${region.id}`">
          {{ region.name || t('remoteDesktop.regionNumber', { n: i + 1 }) }} · {{ region.width }} × {{ region.height }}{{ validDesktopRect(region, width, height) ? '' : ` (${t('remoteDesktop.adjustRegion')})` }}
        </option>
      </optgroup>
      <option v-if="allowCustom" value="create-region">{{ t('remoteDesktop.createRegion') }}</option>
    </select>
    <button v-if="allowCustom && selectedRegion" type="button" :disabled="unavailable" :title="t('remoteDesktop.editRegion')" :aria-label="t('remoteDesktop.editRegion')" @click="emit('editRegion', selectedRegion.id)">
      <Pencil :size="13" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Pencil } from '@lucide/vue'
import { useI18n } from '../i18n'
import { customDesktopRegion, normalizeDesktopView } from '../utils/desktopRegions'
import { validDesktopRect, type DesktopDisplay, type DesktopView } from '../utils/vncDisplays'

const props = defineProps<{
  displays: DesktopDisplay[]
  width: number
  height: number
  view: DesktopView
  allowCustom: boolean
}>()
const emit = defineEmits<{
  select: [view: DesktopView]
  createRegion: []
  editRegion: [id: string]
  focusDesktop: []
}>()
const { t } = useI18n()
const unavailable = computed(() => props.width <= 0 || props.height <= 0)
const regions = computed(() => normalizeDesktopView(props.view).regions!)
const selectedRegion = computed(() => customDesktopRegion(props.view))

function choose(event: Event) {
  const select = event.target as HTMLSelectElement
  const selected = select.value
  select.value = props.view.selected
  if (selected === 'create-region') emit('createRegion')
  else {
    const region = regions.value.find(region => `region:${region.id}` === selected)
    if (region && !validDesktopRect(region, props.width, props.height)) emit('editRegion', region.id)
    else {
      emit('select', { ...props.view, selected })
      emit('focusDesktop')
    }
  }
}
</script>

<style scoped>
.remote-display-select { display: inline-flex; align-items: center; gap: 0.35rem; flex-shrink: 0; font-size: 0.6875rem; }
select { max-width: 15rem; height: 1.25rem; color: var(--text-primary); background: var(--bg-surface); border: 1px solid var(--border-color); border-radius: var(--radius-sm); font: inherit; }
button { display: flex; padding: 0.1rem; border: 0; background: transparent; color: var(--text-secondary); cursor: pointer; }
button:disabled { cursor: default; opacity: 0.4; }
</style>
