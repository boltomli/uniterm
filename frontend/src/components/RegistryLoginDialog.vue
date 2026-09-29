<template>
  <el-dialog
    append-to-body
    v-model="visible"
    :title="t('container.login')"
    width="26rem"
    :close-on-click-modal="false"
  >
    <el-form label-width="5.625rem">
      <el-form-item :label="t('container.loginRegistry')">
        <el-input v-model="form.registry" placeholder="registry.example.com" />
      </el-form-item>
      <el-form-item :label="t('container.loginUsername')" required>
        <el-input v-model="form.username" />
      </el-form-item>
      <el-form-item :label="t('container.loginPassword')" required>
        <el-input v-model="form.password" type="password" show-password />
      </el-form-item>
      <!-- docker/wslc 的 CLI 无 login 级别 TLS 参数，直接不展示该选项 -->
      <el-form-item v-if="!insecureUnsupported" :label="t('container.skipTlsVerify')">
        <el-checkbox v-model="form.insecure" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="visible = false">{{ t('common.cancel') }}</el-button>
      <el-button type="primary" :loading="submitting" @click="onSubmit">{{ t('container.login') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from '../i18n'
import * as client from '../services/containerClient'

// docker/wslc 的 CLI 没有 login 级别的 TLS 参数，insecure 勾选置灰。
const props = defineProps<{ modelValue: boolean; connId: string; runtime: string; presetRegistry?: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'logged-in'): void }>()

const { t } = useI18n()

const visible = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const form = reactive({ registry: '', username: '', password: '', insecure: false })
const submitting = ref(false)
const insecureUnsupported = computed(() => props.runtime === 'docker' || props.runtime === 'wslc')

watch(() => props.modelValue, (v) => {
  if (v) {
    form.registry = props.presetRegistry || ''
    form.username = ''
    form.password = ''
    form.insecure = true
    submitting.value = false
  }
})

async function onSubmit() {
  if (!form.username.trim() || !form.password) {
    ElMessage.error(t('container.loginMissing'))
    return
  }
  submitting.value = true
  try {
    await client.registryLogin(props.connId, {
      registry: form.registry.trim(),
      username: form.username.trim(),
      password: form.password,
      insecure: form.insecure,
    })
    ElMessage.success(t('container.loginSuccess'))
    emit('logged-in')
    visible.value = false
  } catch (e: any) {
    ElMessage.error(String(e?.message || e))
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
</style>
