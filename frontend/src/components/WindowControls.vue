<template>
  <!-- macOS: traffic-light style (close/minimise/zoom as coloured dots) -->
  <div v-if="variant === 'mac'" class="window-controls mac">
      <button class="wc-btn mac close" @click="$emit('close')" :aria-label="t('window.close')" :title="t('window.close')">
        <MacClose />
      </button>
      <button class="wc-btn mac minimise" @click="$emit('minimise')" :aria-label="t('window.minimize')" :title="t('window.minimize')">
        <MacMinimise />
      </button>
      <button class="wc-btn mac maximise" @click="$emit('maximise')" :aria-label="t('window.maximize')" :title="t('window.maximize')">
        <MacRestore v-if="isMaximised" />
        <MacMaximise v-else />
      </button>
  </div>

  <!-- Windows/Linux: match header-btn style -->
  <div v-else class="window-controls">
      <button class="wc-btn win minimise" @click="$emit('minimise')" :aria-label="t('window.minimize')">
        <WinMinimise />
      </button>
      <button class="wc-btn win maximise" @click="$emit('maximise')" :aria-label="t('window.maximize')">
        <WinRestore v-if="isMaximised" />
        <WinMaximise v-else />
      </button>
      <button class="wc-btn win close" @click="$emit('close')" :aria-label="t('window.close')">
        <!-- Same glyph as the dialog close keys (WinClose / --win-close-x) -->
        <WinClose />
      </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from '../i18n'
import WinClose from './icons/WinClose.vue'
import MacClose from './icons/MacClose.vue'
import MacMinimise from './icons/MacMinimise.vue'
import MacMaximise from './icons/MacMaximise.vue'
import MacRestore from './icons/MacRestore.vue'
import WinMinimise from './icons/WinMinimise.vue'
import WinMaximise from './icons/WinMaximise.vue'
import WinRestore from './icons/WinRestore.vue'

const { t } = useI18n()

withDefaults(defineProps<{
  isMaximised: boolean
  /** 'mac' renders traffic-light dots, 'win' the header-btn-matching icons */
  variant?: 'win' | 'mac'
}>(), {
  variant: 'win'
})

defineEmits(['minimise', 'maximise', 'close'])
</script>

<style scoped>
.window-controls {
  display: flex;
  align-items: center;
  gap: 0.125rem;
  --wails-draggable: no-drag;
}

/* ── macOS traffic lights ── */
.window-controls.mac {
  gap: 0.5rem;
  /* nudge right of the header padding so the dots sit clear of the
     window's rounded corners, matching the native macOS inset */
  margin-left: 0.375rem;
}

.wc-btn.mac {
  width: 0.75rem;
  height: 0.75rem;
  padding: 0;
  border: none;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: inset 0 0 0 0.5px rgba(0, 0, 0, 0.15);
}

.wc-btn.mac.close { background: #ff5f57; }
.wc-btn.mac.minimise { background: #febc2e; }
.wc-btn.mac.maximise { background: #28c840; }

/* Glyphs appear when hovering anywhere over the group (macOS behaviour) */
.wc-btn.mac svg {
  width: 0.5rem;
  height: 0.5rem;
  color: rgba(0, 0, 0, 0.55);
  opacity: 0;
  transition: opacity 0.1s ease;
}

.window-controls.mac:hover .wc-btn.mac svg {
  opacity: 1;
}

/* ── Windows/Linux buttons — match header-btn style ── */
.wc-btn.win {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0.3125rem 0.5rem;
  height: 1.75rem;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  transition: all 0.15s ease;
}

.wc-btn.win:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.wc-btn.win svg {
  width: 0.875rem;
  height: 0.875rem;
}

.wc-btn.win.close:hover {
  background: #e81123;
  color: var(--on-accent);
}

.wc-btn.win.close:active {
  background: #f1707a;
}
</style>
