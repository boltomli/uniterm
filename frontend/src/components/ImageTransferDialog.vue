<template>
  <el-dialog
    append-to-body
    v-model="visible"
    :title="mode === 'pull' ? t('container.pull') : t('container.push')"
    width="30rem"
    :close-on-click-modal="false"
  >
    <el-form label-width="5.625rem">
      <el-form-item :label="t('container.colImage')" required :error="imageError">
        <el-input v-model="form.image" :placeholder="mode === 'pull' ? 'nginx:latest' : 'repo/name:tag'" @input="imageError = ''" />
      </el-form-item>
      <template v-if="mode === 'pull'">
        <el-form-item :label="t('container.platform')">
          <el-select
            v-model="form.platform"
            filterable
            allow-create
            default-first-option
            :placeholder="t('container.platformDefault')"
          >
            <el-option :label="t('container.platformDefault')" value="" />
            <el-option v-for="p in platformOptions" :key="p" :label="p" :value="p" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('container.allTags')">
          <el-checkbox v-model="form.allTags" />
        </el-form-item>
      </template>
      <!-- docker/wslc 的 CLI 无单命令级 TLS 参数，直接不展示该选项 -->
      <el-form-item v-if="!insecureUnsupported" :label="t('container.skipTlsVerify')">
        <el-checkbox v-model="form.insecure" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="visible = false">{{ t('common.cancel') }}</el-button>
      <el-button type="primary" @click="onSubmit">{{ mode === 'pull' ? t('container.pull') : t('container.push') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from '../i18n'

// pull/push 共用的传输对话框；insecure 仅 podman/nerdctl 生效，
// docker/wslc 的 CLI 无单命令级参数，勾选置灰并提示走 daemon 配置。
const props = defineProps<{ modelValue: boolean; runtime: string; mode: 'pull' | 'push'; initialImage?: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'start', opts: { image: string; platform: string; insecure: boolean; allTags: boolean }): void }>()

const { t } = useI18n()

const visible = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const form = reactive({ image: '', platform: '', insecure: false, allTags: false })
const imageError = ref('')
const insecureUnsupported = computed(() => props.runtime === 'docker' || props.runtime === 'wslc')

// 常用平台预设；下拉支持手动输入任意 OS/ARCH（allow-create）。
const platformOptions = ['linux/amd64', 'linux/arm64', 'linux/arm/v7', 'linux/386', 'linux/ppc64le', 'linux/s390x']

watch(() => props.modelValue, (v) => {
  if (v) {
    form.image = props.initialImage || ''
    form.platform = ''
    form.insecure = true
    form.allTags = false
    imageError.value = ''
  }
})

function onSubmit() {
  if (!form.image.trim()) {
    imageError.value = t('container.createDialog.imageRequired')
    ElMessage.error(t('container.createDialog.imageRequired'))
    return
  }
  emit('start', {
    image: form.image.trim(),
    platform: form.platform.trim(),
    insecure: form.insecure,
    allTags: form.allTags,
  })
  visible.value = false
}
</script>

<style scoped>
</style>
