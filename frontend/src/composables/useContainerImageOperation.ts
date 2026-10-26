import { ref } from 'vue'
import * as client from '../services/containerClient'
import { backendErrorText } from '../utils/backendError'

// Preparation makes cancellation possible before the load/commit RPC starts.
// A generation guard prevents a cancelled request from updating a reopened dialog.
export function useContainerImageOperation() {
  const busy = ref(false)
  const error = ref('')
  let generation = 0
  let operationId = ''

  async function run(connId: string, execute: (id: string) => Promise<string>): Promise<boolean> {
    if (busy.value) return false
    const attempt = ++generation
    busy.value = true
    error.value = ''
    try {
      const id = await client.prepareImageOperation(connId)
      if (attempt !== generation) {
        await client.cancelImageOperation(id)
        return false
      }
      operationId = id
      await execute(id)
      return attempt === generation
    } catch (e) {
      if (attempt === generation) error.value = backendErrorText(e)
      return false
    } finally {
      if (attempt === generation) {
        if (operationId) void client.cancelImageOperation(operationId).catch(e => { console.error('clean up image operation:', e) })
        operationId = ''
        busy.value = false
      }
    }
  }

  function cancel() {
    generation++
    const id = operationId
    operationId = ''
    busy.value = false
    if (id) void client.cancelImageOperation(id).catch(e => { console.error('cancel image operation:', e) })
  }

  return { busy, error, run, cancel }
}
