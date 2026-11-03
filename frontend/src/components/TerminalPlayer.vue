<template>
  <el-dialog
    v-model="visible"
    :title="t('record.player')"
    width="820px"
    append-to-body
    :close-on-click-modal="false"
    @open="onOpen"
    @closed="onClosed"
  >
    <div class="player-body">
      <div ref="xtermHostRef" class="player-xterm-host"></div>

      <div class="player-controls">
        <el-button
          size="small"
          type="primary"
          :disabled="!ready || events.length === 0"
          @click="togglePlay"
        >
          {{ playing ? t('record.pauseBtn') : t('record.playBtn') }}
        </el-button>

        <span class="player-time">{{ formatTime(currentTime) }} / {{ formatTime(duration) }}</span>

        <el-slider
          v-model="progressPercent"
          :min="0"
          :max="100"
          :step="0.1"
          :disabled="!ready || events.length === 0"
          class="player-slider"
          @input="onSeekInput"
        />

        <span class="player-speed-label">{{ t('record.speed') }}</span>
        <el-select
          v-model="speed"
          size="small"
          class="player-speed"
          @change="onSpeedChange"
        >
          <el-option :value="0.5" label="0.5x" />
          <el-option :value="1" label="1x" />
          <el-option :value="2" label="2x" />
          <el-option :value="4" label="4x" />
        </el-select>
      </div>

      <div v-if="warning" class="player-warning">{{ warning }}</div>
    </div>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, computed, watch, nextTick, onBeforeUnmount } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useI18n } from '../i18n'

import { RecorderGetContent } from '../../bindings/github.com/ys-ll/uniterm/app'
import { parseCastContent, type CastEvent, type ParsedCastHeader } from '../services/terminalRecorder'
import { getXtermTheme } from '../composables/useTerminal'
import { useSettingsStore } from '../stores/settingsStore'

const { t } = useI18n()
const settingsStore = useSettingsStore()

const props = defineProps<{ visible: boolean; filePath: string }>()
const emit = defineEmits<{ 'update:visible': [boolean] }>()

const visible = computed({
  get: () => props.visible,
  set: (v) => emit('update:visible', v),
})

const xtermHostRef = ref<HTMLDivElement>()
let terminal: Terminal | null = null
let fitAddon: FitAddon | null = null

const ready = ref(false)
const playing = ref(false)
const currentTime = ref(0)
const duration = ref(0)
const speed = ref(1)
const progressPercent = ref(0)
const warning = ref('')
const events = ref<CastEvent[]>([])
const header = ref<ParsedCastHeader | null>(null)

let playTimerId: ReturnType<typeof setTimeout> | null = null
let nextEventIdx = 0


async function onOpen() {
  warning.value = ''
  ready.value = false
  playing.value = false
  currentTime.value = 0
  duration.value = 0
  progressPercent.value = 0
  events.value = []
  header.value = null
  nextEventIdx = 0

  // 加载 cast 文件
  let content: string
  try {
    content = await RecorderGetContent(props.filePath)
  } catch (e: any) {
    warning.value = t('record.loadFailed', { error: String(e?.message ?? e) })
    return
  }
  const parsed = parseCastContent(content)
  events.value = parsed.events
  header.value = parsed.header
  duration.value = parsed.duration || 0
  if (events.value.length === 0) {
    warning.value = t('record.emptyList')
    return
  }

  // 初始化 xterm
  await nextTick()
  if (!xtermHostRef.value) return
  if (terminal) {
    terminal.dispose()
    terminal = null
  }
  const themeName = settingsStore.settings.terminal.theme || 'aiops-dark'
  const theme = getXtermTheme(themeName, settingsStore.settings.customTerminalThemes)
  const cols = header.value?.width || 80
  const rows = header.value?.height || 24
  terminal = new Terminal({
    fontSize: 13,
    fontFamily: 'JetBrains Mono Variable, monospace',
    theme,
    cursorBlink: false,
    cursorStyle: 'block',
    cols,
    rows,
    scrollback: 5000,
    allowProposedApi: true,
    disableStdin: true,
  })
  fitAddon = new FitAddon()
  terminal.loadAddon(fitAddon)
  terminal.open(xtermHostRef.value)
  try {
    fitAddon.fit()
  } catch {
    // ignore fit error
  }
  ready.value = true
  // 提示敏感数据
  warning.value = t('record.sensitiveWarning')
}

function onClosed() {
  stopPlay()
  if (terminal) {
    terminal.dispose()
    terminal = null
  }
  fitAddon = null
  ready.value = false
  playing.value = false
  events.value = []
}

function togglePlay() {
  if (playing.value) {
    pause()
  } else {
    play()
  }
}

function play() {
  if (!terminal || events.value.length === 0) return
  playing.value = true
  warning.value = ''
  scheduleNextFrame()
}

function pause() {
  playing.value = false
  if (playTimerId) {
    clearTimeout(playTimerId)
    playTimerId = null
  }
}

function stopPlay() {
  pause()
}

function scheduleNextFrame() {
  if (!playing.value || !terminal) return
  if (nextEventIdx >= events.value.length) {
    playing.value = false
    currentTime.value = duration.value
    progressPercent.value = 100
    return
  }
  const ev = events.value[nextEventIdx]
  // 计算到下一帧的等待时间（毫秒），按倍速缩放。
  const waitMs = Math.max(0, (ev.time - currentTime.value) * 1000 / speed.value)
  playTimerId = setTimeout(() => {
    if (!playing.value || !terminal) return
    // 写入当前帧（仅 "o" 类型，"i" 是输入回放，此处也写入以忠实重现）
    if (ev.type === 'o' || ev.type === 'i') {
      terminal!.write(ev.data)
    }
    currentTime.value = ev.time
    progressPercent.value = duration.value > 0 ? (ev.time / duration.value) * 100 : 0
    nextEventIdx++
    scheduleNextFrame()
  }, waitMs)
}

function onSeekInput(val: number | number[]) {
  if (!terminal || events.value.length === 0) return
  const v = Array.isArray(val) ? val[0] : val
  pause()
  const targetTime = duration.value > 0 ? (v / 100) * duration.value : 0
  // 重置终端并快速重放到 targetTime
  terminal.reset()
  let i = 0
  for (; i < events.value.length; i++) {
    if (events.value[i].time > targetTime) break
    const ev = events.value[i]
    if (ev.type === 'o' || ev.type === 'i') {
      terminal.write(ev.data)
    }
  }
  nextEventIdx = i
  currentTime.value = targetTime
  progressPercent.value = v
}

function onSpeedChange() {
  // 倍速改变后，若在播放，重新调度下一帧以应用新速率。
  if (playing.value) {
    if (playTimerId) {
      clearTimeout(playTimerId)
      playTimerId = null
    }
    scheduleNextFrame()
  }
}

function formatTime(sec: number): string {
  if (!sec || sec <= 0) return '00:00'
  const total = Math.floor(sec)
  const mm = Math.floor(total / 60).toString().padStart(2, '0')
  const ss = (total % 60).toString().padStart(2, '0')
  return `${mm}:${ss}`
}

// visible 关闭时清理
watch(() => props.visible, (v) => {
  if (!v) {
    onClosed()
  }
})

onBeforeUnmount(() => {
  onClosed()
})

</script>

<style scoped>
.player-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.player-xterm-host {
  width: 100%;
  height: 420px;
  background: #000;
  border-radius: 6px;
  overflow: hidden;
}
.player-controls {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.player-time {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-secondary);
  min-width: 100px;
}
.player-slider {
  flex: 1;
  min-width: 200px;
}
.player-speed-label {
  font-size: 12px;
  color: var(--text-secondary);
}
.player-speed {
  width: 90px;
}
.player-warning {
  font-size: 12px;
  color: var(--el-color-warning);
  padding: 8px 12px;
  background: var(--el-fill-color-light);
  border-radius: 4px;
}
</style>