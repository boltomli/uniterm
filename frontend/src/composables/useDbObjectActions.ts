import { ref } from 'vue'
import { msg } from '../services/message'

// Shared state machine for destructive database-object actions (DB-28,
// 2026-10-25 regression audit): DBTreePanel (context menu), DBObjectList
// (inline buttons) and DBTableStructure each re-implemented
// "confirm → execute → refresh → notify" with drifting error handling and
// refresh calls. The composable owns the confirm dialog and the flow; the
// components only trigger it and render the dialog from the exposed refs.

export interface DbObjectRef {
  dbName: string
  tableName?: string
  kind: 'table' | 'view' | 'database'
}

export function useDbObjectActions(handlers: {
  /** Refresh whatever UI owns the object list after a successful action. */
  onRefresh: (dbName: string) => void | Promise<void>
  /** Bubble to the parent (e.g. to close open docs of a dropped table). */
  onObjectRemoved?: (payload: DbObjectRef) => void
  /** Optional extra work after a successful action (e.g. activate a doc). */
  onDone?: (payload: DbObjectRef) => void
}) {
  const confirmVisible = ref(false)
  const confirmTitle = ref('')
  const confirmText = ref('')
  const confirmName = ref('')
  const confirmInput = ref('')
  let confirmAction: (() => Promise<void>) | null = null

  function showConfirm(title: string, text: string, name: string, action: () => Promise<void>) {
    confirmTitle.value = title
    confirmText.value = text
    confirmName.value = name
    confirmInput.value = ''
    confirmAction = action
    confirmVisible.value = true
  }

  async function onConfirm() {
    if (confirmAction) {
      try {
        await confirmAction()
      } catch (e: any) {
        // One place decides how action failures surface (DB-28): visible
        // toast, not a stray console.error.
        msg.error(e?.message || String(e))
      }
    }
    confirmVisible.value = false
  }

  /** The unified flow: confirm → execute → refresh → notify. */
  function runObjectAction(
    target: DbObjectRef,
    labels: { title: string; text: string; name: string },
    action: () => Promise<void>,
  ) {
    showConfirm(labels.title, labels.text, labels.name, async () => {
      await action()
      await handlers.onRefresh(target.dbName)
      handlers.onObjectRemoved?.(target)
      handlers.onDone?.(target)
    })
  }

  return {
    confirmVisible,
    confirmTitle,
    confirmText,
    confirmName,
    confirmInput,
    showConfirm,
    onConfirm,
    runObjectAction,
  }
}
