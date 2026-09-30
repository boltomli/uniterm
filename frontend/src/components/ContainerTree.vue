<template>
  <div class="db-tree-panel">
    <div class="tree-content">
      <div
        v-for="item in items"
        :key="item.key"
        class="table-item"
        :class="{ selected: modelValue === item.key }"
        @click="$emit('update:modelValue', item.key)"
      >
        <component :is="item.icon" class="table-icon" :size="lucideSize('0.875rem')" />
        <span class="table-name">{{ t('container.' + item.key) }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { Gauge, Boxes, Layers } from '@lucide/vue'
import { useI18n } from '../i18n'
import { lucideSize } from '../utils/lucideSize'

// 三个一级视图：总览、容器、镜像；key 对应 container.* 的 i18n key。
defineProps<{ modelValue: string }>()
defineEmits<{ (e: 'update:modelValue', key: string): void }>()

const { t } = useI18n()

const items = [
  { key: 'overview', icon: Gauge },
  { key: 'containers', icon: Boxes },
  { key: 'images', icon: Layers },
]
</script>

<style scoped>
/* 复用 DBTreePanel/K8sTree 的样式规则；class 同名，粘贴过来避免跨组件 scoped 冲突。 */
.db-tree-panel {
  height: 100%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.tree-content {
  flex: 1;
  overflow: auto;
  padding-top: 0.25rem;
}
.table-item {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  padding: 0.375rem 0.625rem;
  user-select: none;
  transition: background 0.12s ease;
}
.table-item:hover {
  background: var(--bg-hover);
}
.table-item.selected {
  background: var(--bg-hover);
}
.table-icon {
  flex-shrink: 0;
  color: var(--text-muted);
}
.table-item.selected .table-icon {
  color: var(--text-primary);
}
.table-name {
  font-family: var(--font-ui);
  font-size: 0.8125rem;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
