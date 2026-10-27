<template>
  <el-dialog append-to-body v-model="visible" :title="t('container.commitImage')" width="34rem" :close-on-click-modal="false">
    <el-form label-width="7rem" :disabled="busy" @submit.prevent="submit">
      <el-form-item :label="t('container.colName')">
        <span class="target-name">{{ target?.name }}</span>
      </el-form-item>
      <el-form-item :label="t('container.commitTarget')" required>
        <el-input v-model="image" placeholder="my-repo/my-image:tag" />
      </el-form-item>
      <el-form-item :label="t('container.commitAuthor')">
        <el-input v-model="author" />
      </el-form-item>
      <el-form-item :label="t('container.commitMessage')">
        <el-input v-model="message" type="textarea" :rows="2" />
      </el-form-item>
    </el-form>
    <p class="operation-hint">{{ t('container.commitHint') }}</p>
    <p v-if="busy" class="operation-status" role="status">{{ t('container.committingImage') }}</p>
    <p v-if="error" class="operation-error" role="alert">{{ error }}</p>
    <p v-if="busy" class="operation-hint">{{ t('container.imageCancelHint') }}</p>
    <template #footer>
      <el-button @click="visible = false">{{ t('common.cancel') }}</el-button>
      <el-button type="primary" :loading="busy" :disabled="busy || !image.trim()" @click="submit">{{ t('container.commitImage') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from '../i18n'
import { commitImage } from '../services/containerClient'
import { useContainerImageOperation } from '../composables/useContainerImageOperation'
import type { ContainerInfo } from '../types/container'

const props = defineProps<{ modelValue: boolean; connId: string; target: ContainerInfo | null }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void; (e: 'committed'): void }>()
const { t } = useI18n()
const { busy, error, run, cancel } = useContainerImageOperation()
const image = ref('')
const author = ref('')
const message = ref('')
const visible = computed({ get: () => props.modelValue, set: value => { if (!value) cancel(); emit('update:modelValue', value) } })
watch(() => props.modelValue, open => {
  if (open) { image.value = ''; author.value = ''; message.value = ''; error.value = '' }
  else cancel()
})
watch(() => props.connId, () => { cancel(); visible.value = false })
onBeforeUnmount(cancel)

async function submit() {
  const id = props.target?.id
  if (!id || !props.connId || !image.value.trim() || busy.value) return
  const opts = { image: image.value.trim(), author: author.value.trim(), message: message.value.trim() }
  if (await run(props.connId, operationId => commitImage(operationId, id, opts))) {
    ElMessage.success(t('container.imageCommitted'))
    emit('committed')
    visible.value = false
  }
}
</script>

<style scoped>
.target-name {
  font-family: var(--font-mono, monospace);
  overflow-wrap: anywhere;
}
.operation-hint { color: var(--text-muted); font-size: 12px; line-height: 1.5; }
.operation-status { margin: 0.25rem 0 0; color: var(--text-muted); font-size: 12px; line-height: 1.5; }
.operation-error { color: var(--el-color-danger); overflow-wrap: anywhere; }
</style>
