<template>
  <el-dialog
    append-to-body
    v-model="visible"
    :title="state.title"
    width="25rem"
    :close-on-click-modal="false"
    @open="onOpen"
    @close="onCancel"
  >
    <el-form label-width="5rem" @submit.prevent="onConfirm">
      <el-form-item :label="t('conn.name')">
        <el-input
          ref="nameInputRef"
          v-model="state.name"
          :placeholder="t('conn.namePlaceholder')"
          @keyup.enter="onConfirm"
        />
      </el-form-item>
      <el-form-item :label="t('conn.group')">
        <el-tree-select
          v-model="groupValue"
          :data="groupTreeData"
          :render-after-expand="false"
          check-strictly
          clearable
          :placeholder="t('conn.noGroup')"
          style="width:100%"
        />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="onCancel">{{ t('conn.cancel') }}</el-button>
      <el-button type="primary" :disabled="!state.name.trim()" @click="onConfirm">{{ t('conn.save') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
// Shared「工作区名称 + 分组」dialog (save / edit / create-empty workspace).
// State lives in composables/workspaceNameDialog.ts; this component is the
// single mounted instance (App.vue).
import { computed, ref, nextTick } from 'vue'
import { useI18n } from '../i18n'
import { useConnectionStore, type GroupTreeNode } from '../stores/connectionStore'
import { useWorkspaceNameDialogState, finishWorkspaceNameDialog } from '../composables/workspaceNameDialog'

const { t } = useI18n()
const connectionStore = useConnectionStore()

const state = useWorkspaceNameDialogState()
const nameInputRef = ref<{ focus: () => void } | null>(null)

// el-tree-select uses sentinel values; map them to/from the undefined
// "no group" state, same convention as the sidebar move-to dialog.
const NO_GROUP = '__none__'
const groupValue = ref<string>(NO_GROUP)

const visible = computed({
  get: () => state.visible,
  set: (v: boolean) => { if (!v) return } // close always goes through @close → onCancel
})

// Re-pick the group tree on each open so newly created groups appear.
interface TreeOption {
  value: string
  label: string
  children?: TreeOption[]
}

const groupTreeData = computed<TreeOption[]>(() => {
  function buildTree(nodes: GroupTreeNode[]): TreeOption[] {
    return nodes.map(node => ({
      value: node.group.id,
      label: node.group.name,
      children: node.children.length > 0 ? buildTree(node.children) : undefined,
    }))
  }
  return [
    { value: NO_GROUP, label: t('conn.noGroup') },
    ...buildTree(connectionStore.groupedConnections.roots),
  ]
})

function onOpen() {
  groupValue.value = state.groupId || NO_GROUP
  nextTick(() => nameInputRef.value?.focus())
}

function onConfirm() {
  const name = state.name.trim()
  if (!name) return
  finishWorkspaceNameDialog({ name, groupId: groupValue.value === NO_GROUP ? undefined : groupValue.value })
}

function onCancel() {
  finishWorkspaceNameDialog(null)
}
</script>
