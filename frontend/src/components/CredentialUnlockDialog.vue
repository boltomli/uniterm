<template>
  <el-dialog append-to-body
    :model-value="visible"
    @update:model-value="(v: boolean) => !v && onCancel()"
    :title="t('unlock.title')"
    width="25rem"
    :close-on-click-modal="false"
    :close-on-press-escape="false"
    :show-close="false"
  >
    <p v-if="cred.status.decryptFailed" class="form-hint">{{ t('unlock.decryptFailedHint') }}</p>
    <p v-else-if="cred.status.mode === 'keychain'" class="form-hint">{{ t('keychainLost.desc') }}</p>
    <p v-else-if="!cred.status.mode" class="form-hint">{{ t('unlock.modeUnknownHint') }}</p>
    <el-input v-if="cred.status.mode === 'master-password'" v-model="pw" type="password" show-password :placeholder="t('unlock.password')" @keyup.enter="onUnlock" />
    <div v-if="errorMsg" class="form-error">{{ errorMsg }}</div>
    <template #footer>
      <el-button @click="onQuit">{{ t('unlock.quit') }}</el-button>
      <el-button @click="onReset">{{ t('unlock.reset') }}</el-button>
      <el-button v-if="cred.status.mode === 'master-password'" type="primary" :loading="submitting" @click="onUnlock">{{ t('unlock.confirm') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from '../i18n'
import { QuitApp } from '../../bindings/github.com/ys-ll/uniterm/app'
import { useCredentialStore } from '../stores/credentialStore'

defineProps<{ visible: boolean }>()
const emit = defineEmits<{ (e: 'update:visible', v: boolean): void; (e: 'done'): void; (e: 'reset'): void }>()

const { t } = useI18n()
const cred = useCredentialStore()
const pw = ref('')
const submitting = ref(false)
const errorMsg = ref('')

async function onUnlock() {
  errorMsg.value = ''
  submitting.value = true
  try {
    await cred.unlock(pw.value)
    emit('done')
    emit('update:visible', false)
  } catch (e: any) {
    errorMsg.value = e?.message || String(e)
  } finally {
    submitting.value = false
  }
}
function onReset() { emit('reset') }
async function onQuit() {
  submitting.value = true
  try {
    await QuitApp()
  } finally {
    submitting.value = false
  }
}
function onCancel() { emit('update:visible', false) }
</script>

<style scoped>
.form-error { color: var(--el-color-danger); font-size: 0.8125rem; margin-top: 0.5rem; }
.form-hint { color: var(--el-text-color-secondary); font-size: 0.8125rem; margin: 0 0 0.5rem; }
</style>
