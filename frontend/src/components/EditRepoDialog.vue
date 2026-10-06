<template>
  <el-dialog append-to-body
    v-model="visible"
    :title="t('editRepo.title')"
    width="32.5rem"
    :close-on-click-modal="false"
    @close="handleClose"
  >
    <el-form label-width="7.5rem" class="edit-repo-form">
      <el-form-item :label="t('addRepo.backend')">
        <div class="locked-field">
          <span class="locked-value">{{ isWebdav ? t('addRepo.backendWebDAV') : t('addRepo.backendGit') }}</span>
          <el-icon class="lock-icon"><Lock :size="lucideSize('0.875rem')" /></el-icon>
        </div>
        <div class="form-hint">{{ t('addRepo.backendSwitchHint') }}</div>
      </el-form-item>

      <template v-if="!isWebdav">
        <el-form-item :label="t('editRepo.url')">
          <div class="locked-field">
            <span class="locked-value">{{ syncStore.config.repoUrl }}</span>
            <el-icon class="lock-icon"><Lock :size="lucideSize('0.875rem')" /></el-icon>
          </div>
          <div class="form-hint">{{ t('editRepo.urlLocked') }}</div>
        </el-form-item>

        <el-form-item :label="t('editRepo.username')">
          <div class="locked-field">
            <span class="locked-value">{{ syncStore.config.username }}</span>
            <el-icon class="lock-icon"><Lock :size="lucideSize('0.875rem')" /></el-icon>
          </div>
        </el-form-item>

        <el-form-item :label="t('editRepo.token')">
          <el-input
            v-model="token"
            type="password"
            show-password
            :placeholder="t('editRepo.tokenPlaceholder')"
          />
          <div class="form-hint">{{ t('editRepo.tokenHint') }}</div>
        </el-form-item>
      </template>

      <template v-else>
        <el-form-item :label="t('addRepo.serverUrl')">
          <div class="locked-field">
            <span class="locked-value">{{ syncStore.config.webdavServer }}</span>
            <el-icon class="lock-icon"><Lock :size="lucideSize('0.875rem')" /></el-icon>
          </div>
          <div class="form-hint">{{ t('editRepo.webdavTargetLocked') }}</div>
        </el-form-item>

        <el-form-item :label="t('addRepo.basePath')">
          <div class="locked-field">
            <span class="locked-value">{{ syncStore.config.webdavPath }}</span>
            <el-icon class="lock-icon"><Lock :size="lucideSize('0.875rem')" /></el-icon>
          </div>
        </el-form-item>

        <el-form-item :label="t('addRepo.webdavUser')">
          <div class="locked-field">
            <span class="locked-value">{{ syncStore.config.webdavUser }}</span>
            <el-icon class="lock-icon"><Lock :size="lucideSize('0.875rem')" /></el-icon>
          </div>
        </el-form-item>

        <el-form-item :label="t('addRepo.webdavPassword')">
          <el-input
            v-model="webdavPassword"
            type="password"
            show-password
          />
          <div class="form-hint">{{ t('editRepo.webdavPasswordHint') }}</div>
        </el-form-item>
      </template>

      <el-form-item :label="t('editRepo.currentPassword')">
        <el-input
          v-model="currentPassword"
          type="password"
          show-password
          :placeholder="t('editRepo.currentPasswordPlaceholder')"
        />
        <div class="form-hint">{{ isWebdav ? t('addRepo.masterPasswordHint') : t('editRepo.currentPasswordHint') }}</div>
      </el-form-item>
    </el-form>

    <div v-if="errorMsg" class="form-error">{{ errorMsg }}</div>

    <template #footer>
      <el-button @click="handleClose">{{ t('common.cancel') }}</el-button>
      <el-button type="primary" :loading="submitting" @click="handleSubmit">
        {{ t('common.save') }}
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { lucideSize } from '../utils/lucideSize'
import { ref, computed, watch } from 'vue'
import { Lock } from '@lucide/vue'
import { useI18n } from '../i18n'
import { useSyncStore } from '../stores/syncStore'
import { SyncVerifyPassword, SyncUpdateWebDAVPassword } from '../../bindings/github.com/ys-ll/uniterm/app'
import { backendErrorText } from '../utils/backendError'
import { msg } from '../services/message'

const { t } = useI18n()
const syncStore = useSyncStore()

const visible = computed({
  get: () => syncStore.showEditRepo,
  set: (v) => { if (!v) syncStore.showEditRepo = false },
})

const token = ref('')
const webdavPassword = ref('')
const currentPassword = ref('')
const submitting = ref(false)
const errorMsg = ref('')
const isWebdav = computed(() => syncStore.config.backend === 'webdav')

watch(visible, (v) => {
  if (v) {
    token.value = ''
    webdavPassword.value = ''
    currentPassword.value = ''
    errorMsg.value = ''
  }
})

function handleClose() {
  syncStore.showEditRepo = false
  resetForm()
}

function resetForm() {
  token.value = ''
  webdavPassword.value = ''
  currentPassword.value = ''
  errorMsg.value = ''
}

async function handleSubmit() {
  errorMsg.value = ''

  // WebDAV: only the credential is editable — the server / remote folder /
  // username are locked, mirroring the locked repo URL in the git branch.
  // Same shape as the git flow: verify (probe new password + master
  // password against the remote snapshot) → save → sync.
  if (isWebdav.value) {
    if (!currentPassword.value) {
      errorMsg.value = t('editRepo.currentPasswordRequired')
      return
    }

    submitting.value = true
    try {
      await SyncUpdateWebDAVPassword(webdavPassword.value, currentPassword.value)
      const syncResult = await syncStore.doSync()
      if (syncResult) {
        if (syncResult.direction === 3) {
          // Conflict — SyncConflictDialog will open via event
        } else {
          msg.success(syncResult.message || t('editRepo.success'))
        }
      } else {
        msg.error(syncStore.lastResult || t('settings.syncFailed'))
      }
      syncStore.showEditRepo = false
      resetForm()
    } catch (e: any) {
      // Coded backend errors (e.g. master_password_mismatch) are localized
      // by backendErrorText; anything else passes through raw.
      errorMsg.value = backendErrorText(e)
    } finally {
      submitting.value = false
    }
    return
  }

  submitting.value = true
  try {
    await SyncVerifyPassword(currentPassword.value, syncStore.config.username, token.value)
    await syncStore.saveConfig(token.value)
    const syncResult = await syncStore.doSync()
    if (syncResult) {
      if (syncResult.direction === 3) {
        // Conflict — SyncConflictDialog will open via event
      } else {
        msg.success(syncResult.message || t('editRepo.success'))
      }
    } else {
      msg.error(syncStore.lastResult || t('settings.syncFailed'))
    }
    syncStore.showEditRepo = false
    resetForm()
  } catch (e: any) {
    const msg = backendErrorText(e)
    errorMsg.value = msg === 'WRONG_SYNC_PASSWORD' ? t('editRepo.wrongPassword') : msg
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.edit-repo-form {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}

.locked-field {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0.75rem;
  background: var(--el-fill-color-light);
  border-radius: var(--radius-sm);
  font-size: 0.8125rem;
  font-family: var(--font-mono);
  color: var(--text-secondary);
  word-break: break-all;
}

.locked-value {
  flex: 1;
  min-width: 0;
}

.lock-icon {
  flex-shrink: 0;
  color: var(--text-muted);
}

.form-hint {
  font-size: 0.75rem;
  color: var(--text-muted);
  margin-top: 0.25rem;
  line-height: 1.4;
}

.form-error {
  color: var(--el-color-danger);
  font-size: 0.8125rem;
  margin-top: 0.5rem;
}
</style>
