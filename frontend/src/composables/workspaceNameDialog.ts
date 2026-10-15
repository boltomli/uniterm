// Shared「工作区名称 + 分组」dialog service. The dialog itself is mounted once
// (App.vue); callers open it through openWorkspaceNameDialog and await the
// result. Used by 保存工作区 / 编辑工作区 / 分组右键新建工作区 so all three
// flows share the same name + group picker.
import { reactive } from 'vue'

export interface WorkspaceNameDialogResult {
  name: string
  groupId: string | undefined
}

interface WorkspaceNameDialogState {
  visible: boolean
  title: string
  name: string
  groupId: string | undefined
}

const state = reactive<WorkspaceNameDialogState>({
  visible: false,
  title: '',
  name: '',
  groupId: undefined
})

let resolver: ((r: WorkspaceNameDialogResult | null) => void) | null = null

// Open the dialog and resolve with { name, groupId } on confirm, or null on
// cancel / close. A second open while pending resolves the first as null.
export function openWorkspaceNameDialog(opts: { title: string; initialName?: string; initialGroupId?: string }): Promise<WorkspaceNameDialogResult | null> {
  resolver?.(null)
  state.title = opts.title
  state.name = opts.initialName ?? ''
  state.groupId = opts.initialGroupId
  state.visible = true
  return new Promise(resolve => { resolver = resolve })
}

// Called by the dialog component on confirm / cancel.
export function finishWorkspaceNameDialog(result: WorkspaceNameDialogResult | null) {
  state.visible = false
  resolver?.(result)
  resolver = null
}

// For the mounted dialog component.
export function useWorkspaceNameDialogState() {
  return state
}
