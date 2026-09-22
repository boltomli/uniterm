<template>
  <el-dialog
    append-to-body
    :model-value="visible"
    :title="t('conn.sshAlgoDialogTitle')"
    width="46rem"
    @update:model-value="(v: boolean) => emit('update:visible', v)"
    @opened="onOpened"
  >
    <div v-if="error" class="algo-error">{{ error }}</div>
    <el-tabs v-model="activeTab">
      <el-tab-pane v-for="dim in dimensions" :key="dim.key" :name="dim.key">
        <template #label>
          <span :class="{ 'algo-tab-invalid': draft[dim.key].length === 0 }">{{ t(dim.label) }}</span>
        </template>
        <div class="algo-dim-hint">{{ t('conn.sshAlgoOrderHint') }}</div>
        <div v-if="draft[dim.key].length === 0" class="algo-empty">{{ t('conn.sshAlgoEmptyList') }}</div>
        <div class="algo-list">
          <div v-for="(id, idx) in draft[dim.key]" :key="id" class="algo-item">
            <span class="algo-index">{{ idx + 1 }}</span>
            <span class="algo-id">{{ id }}</span>
            <el-tag :type="levelTag(levelOf(dim.key, id))" size="small">{{ t(levelLabel(levelOf(dim.key, id))) }}</el-tag>
            <span class="algo-actions">
              <el-button size="small" text :disabled="idx === 0" @click="move(dim.key, idx, -1)">↑</el-button>
              <el-button size="small" text :disabled="idx === draft[dim.key].length - 1" @click="move(dim.key, idx, 1)">↓</el-button>
              <el-button size="small" text type="danger" @click="remove(dim.key, idx)">
                <Trash2 size="13" />
              </el-button>
            </span>
          </div>
        </div>
        <div class="algo-add">
          <span>{{ t('conn.sshAlgoAdd') }}</span>
          <el-select
            v-model="addSelection[dim.key]"
            filterable
            size="small"
            class="algo-add-select"
            @change="onAddSelect(dim.key)"
          >
            <el-option
              v-for="opt in candidates(dim.key)"
              :key="opt.id"
              :value="opt.id"
              :label="opt.id"
            >
              <span class="algo-option">{{ opt.id }}</span>
              <el-tag :type="levelTag(opt.level)" size="small">{{ t(levelLabel(opt.level)) }}</el-tag>
            </el-option>
          </el-select>
        </div>
      </el-tab-pane>
    </el-tabs>
    <template #footer>
      <el-button @click="restoreDefaults">{{ t('conn.sshAlgoRestoreDefault') }}</el-button>
      <el-button @click="emit('update:visible', false)">{{ t('common.cancel') }}</el-button>
      <el-button type="primary" :disabled="!isValid" @click="onSave">{{ t('common.confirm') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from '../i18n'
import { GetSupportedSSHAlgorithms } from '../../bindings/github.com/ys-ll/uniterm/app'
import type { SSHAlgoConfig } from '../types/session'
import { Trash2 } from '@lucide/vue'

type DimKey = 'keyExchanges' | 'ciphers' | 'macs' | 'hostKeys'

interface AlgoOption { id: string; level: string }

const props = defineProps<{ visible: boolean; config: SSHAlgoConfig | undefined }>()
const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
  (e: 'save', config: SSHAlgoConfig): void
}>()

const { t } = useI18n()

const dimensions: { key: DimKey; label: string }[] = [
  { key: 'keyExchanges', label: 'conn.sshAlgoTabKex' },
  { key: 'ciphers', label: 'conn.sshAlgoTabCiphers' },
  { key: 'macs', label: 'conn.sshAlgoTabMacs' },
  { key: 'hostKeys', label: 'conn.sshAlgoTabHostKeys' },
]

const activeTab = ref<DimKey>('ciphers')
const error = ref('')
const pool = reactive<Record<DimKey, AlgoOption[]>>({
  keyExchanges: [],
  ciphers: [],
  macs: [],
  hostKeys: [],
})
const compatiblePreset = reactive<Record<DimKey, string[]>>({
  keyExchanges: [],
  ciphers: [],
  macs: [],
  hostKeys: [],
})

const draft = reactive<Record<DimKey, string[]>>({
  keyExchanges: [],
  ciphers: [],
  macs: [],
  hostKeys: [],
})
const addSelection = reactive<Record<DimKey, string>>({
  keyExchanges: '',
  ciphers: '',
  macs: '',
  hostKeys: '',
})

const isValid = computed(() => dimensions.every(d => draft[d.key].length > 0))

function levelOf(dim: DimKey, id: string): string {
  return pool[dim].find(o => o.id === id)?.level ?? 'modern'
}
function levelLabel(level: string): string {
  if (level === 'legacy') return 'conn.sshAlgoLevelLegacy'
  if (level === 'insecure') return 'conn.sshAlgoLevelInsecure'
  return 'conn.sshAlgoLevelModern'
}
function levelTag(level: string): 'success' | 'warning' | 'danger' | 'info' {
  if (level === 'legacy') return 'warning'
  if (level === 'insecure') return 'danger'
  return 'success'
}
function candidates(dim: DimKey): AlgoOption[] {
  const selected = new Set(draft[dim])
  return pool[dim].filter(o => !selected.has(o.id))
}
function move(dim: DimKey, idx: number, delta: number) {
  const target = idx + delta
  if (target < 0 || target >= draft[dim].length) return
  const list = draft[dim]
  ;[list[idx], list[target]] = [list[target], list[idx]]
}
function remove(dim: DimKey, idx: number) {
  draft[dim].splice(idx, 1)
}
function add(dim: DimKey) {
  const id = addSelection[dim]
  if (!id || draft[dim].includes(id)) return
  draft[dim].push(id)
  addSelection[dim] = ''
}
function onAddSelect(dim: DimKey) {
  add(dim)
}
function restoreDefaults() {
  for (const d of dimensions) {
    draft[d.key] = [...compatiblePreset[d.key]]
  }
}

async function onOpened() {
  error.value = ''
  let supported
  try {
    supported = await GetSupportedSSHAlgorithms()
  } catch (e) {
    error.value = String(e)
    return
  }
  pool.keyExchanges = supported?.keyExchanges ?? []
  pool.ciphers = supported?.ciphers ?? []
  pool.macs = supported?.macs ?? []
  pool.hostKeys = supported?.hostKeys ?? []
  compatiblePreset.keyExchanges = [...(supported?.compatible?.keyExchanges ?? [])]
  compatiblePreset.ciphers = [...(supported?.compatible?.ciphers ?? [])]
  compatiblePreset.macs = [...(supported?.compatible?.macs ?? [])]
  compatiblePreset.hostKeys = [...(supported?.compatible?.hostKeys ?? [])]
  // An empty draft (first entry into custom mode) starts from the
  // compatible preset as the base to edit down from.
  const incoming = props.config
  for (const d of dimensions) {
    const existing = incoming?.[d.key]
    draft[d.key] = existing && existing.length > 0 ? [...existing] : [...compatiblePreset[d.key]]
  }
}

function onSave() {
  if (!isValid.value) {
    error.value = t('conn.sshAlgoEmptyList')
    return
  }
  emit('save', {
    mode: 'custom',
    keyExchanges: [...draft.keyExchanges],
    ciphers: [...draft.ciphers],
    macs: [...draft.macs],
    hostKeys: [...draft.hostKeys],
  })
  emit('update:visible', false)
}
</script>

<style scoped>
.algo-dim-hint {
  margin-bottom: 0.5rem;
  font-size: 0.6875rem;
  color: var(--el-text-color-secondary);
}
.algo-list {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 0.375rem;
  max-height: 18rem;
  overflow-y: auto;
}
.algo-item {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.35rem 0.75rem;
  font-size: 0.8125rem;
}
.algo-item + .algo-item {
  border-top: 1px solid var(--el-border-color-lighter);
}
.algo-index {
  width: 1.25rem;
  color: var(--el-text-color-secondary);
  font-size: 0.6875rem;
  text-align: right;
}
.algo-id {
  flex: 1;
  font-family: var(--font-mono, ui-monospace, "JetBrains Mono", monospace);
  word-break: break-all;
}
.algo-actions {
  display: flex;
  gap: 0.125rem;
}
.algo-actions .el-button + .el-button {
  margin-left: 0;
}
.algo-add {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-top: 0.6rem;
  font-size: 0.8125rem;
}
.algo-add-select {
  flex: 1;
}
.algo-option {
  font-family: var(--font-mono, ui-monospace, "JetBrains Mono", monospace);
  margin-right: 0.5rem;
}
.algo-empty {
  color: var(--el-color-danger);
  font-size: 0.6875rem;
  padding: 0.4rem 0;
}
.algo-error {
  color: var(--el-color-danger);
  font-size: 0.6875rem;
  margin-bottom: 0.5rem;
}
.algo-tab-invalid {
  color: var(--el-color-danger);
}
</style>
