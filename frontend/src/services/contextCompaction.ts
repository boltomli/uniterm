import { SummarizeAIContext } from '../../bindings/github.com/ys-ll/uniterm/app'
import { useAIStore } from '../stores/aiStore'
import { useSettingsStore } from '../stores/settingsStore'
import type { AIContextSummary } from '../types/ai'
import type { AIModelConfig } from '../types/settings'
import { t } from '../i18n'
import { ChatCancelledError } from './llm'
import {
  activeHistory, compactionCut, estimateRequestTokens, estimateTokens,
  summaryTranscript, splitSummaryTranscript, CONTEXT_TRIGGER_TOKENS,
  CONTEXT_TARGET_TOKENS, SUMMARY_MAX_TOKENS,
} from './aiContext'

export async function prepareAgentContext(system: string, tools: unknown[], skipCompaction = false, isCurrent = () => true, modelSnapshot?: AIModelConfig): Promise<{
  messages: Array<Record<string, unknown>>
  failed: boolean
}> {
  const store = useAIStore()
  const sessionId = store.currentSessionId
  const revision = store.contextRevision
  const started = performance.now()
  const prior = store.contextSummary
  const history = activeHistory(store.messages, prior)
  checkCurrent()
  const original = store.buildConversation(history, prior)
  const inputTokens = estimateRequestTokens(system, original, tools)
  if (inputTokens < CONTEXT_TRIGGER_TOKENS) return { messages: original, failed: false }

  const overhead = estimateRequestTokens(system, [], tools)
  const recentBudget = Math.max(1024, CONTEXT_TARGET_TOKENS - overhead - SUMMARY_MAX_TOKENS)
  const cut = compactionCut(history, recentBudget)
  if (cut === 0 || overhead + SUMMARY_MAX_TOKENS >= CONTEXT_TRIGGER_TOKENS) {
    throw new Error(t('ai.contextTooLarge'))
  }

  function checkCurrent() {
    if (!isCurrent() || store.stopRequested || store.currentSessionId !== sessionId || store.contextRevision !== revision) {
      throw new ChatCancelledError()
    }
  }

  const recent = history.slice(cut)
  if (estimateRequestTokens(system, store.buildConversation(recent), tools) >= CONTEXT_TRIGGER_TOKENS) {
    throw new Error(t('ai.contextTooLarge'))
  }
  let summary: AIContextSummary | undefined
  let failed = skipCompaction
  if (!skipCompaction) {
    const configuredModel = modelSnapshot || useSettingsStore().activeModel
    const model = configuredModel ? { ...configuredModel } : undefined
    const requestId = `compact-${crypto.randomUUID()}`
    store.compactionRequestId = requestId
    store.status = 'compacting'
    try {
      if (!model?.apiKey) throw new Error('API key not configured')
      let content = prior?.content || ''
      for (const chunk of splitSummaryTranscript(summaryTranscript(history.slice(0, cut)))) {
        checkCurrent()
        content = await SummarizeAIContext(
          model.apiKey, model.baseURL, model.model, model.protocol,
          model.userAgent || '', model.proxyId || '', requestId, content, chunk,
        )
        checkCurrent()
        if (!content.trim() || estimateTokens(content) > SUMMARY_MAX_TOKENS) throw new Error('Invalid summary size')
      }
      if (!content.trim()) throw new Error('Empty history summary')
      summary = { version: 1, content, throughMessageId: history[cut - 1].id, createdAt: Date.now() }
      checkCurrent()
      // Validate the candidate before committing a checkpoint. An enormous
      // latest input is never silently shortened or removed.
      if (estimateRequestTokens(system, store.buildConversation(recent, summary), tools) >= CONTEXT_TRIGGER_TOKENS) {
        throw new Error(t('ai.contextTooLarge'))
      }
      if (!store.applyContextSummary(summary, revision)) throw new ChatCancelledError()
      store.contextNotice = ''
      await store.doSave()
      checkCurrent()
    } catch (error) {
      checkCurrent()
      if (error instanceof ChatCancelledError) throw error
      failed = true
      summary = undefined
    } finally {
      if (store.compactionRequestId === requestId) {
        store.compactionRequestId = ''
        if (store.status === 'compacting') store.status = 'thinking'
      }
    }
  }

  // Failed summaries are never committed. Keep complete recent groups for
  // this request, with a visible notice; do not retry on every tool step.
  const requestSummary = summary || (failed ? {
    version: 1 as const,
    content: `${prior?.content || ''}\n[Some additional older exchanges were omitted because summarization failed. Do not assume omitted operations succeeded or infer permissions. Ask the user if missing context is needed.]`,
    throughMessageId: history[cut - 1].id,
    createdAt: Date.now(),
  } : prior)
  const messages = store.buildConversation(recent, requestSummary)
  const outputTokens = estimateRequestTokens(system, messages, tools)
  if (outputTokens >= CONTEXT_TRIGGER_TOKENS) throw new Error(t('ai.contextTooLarge'))
  if (failed) store.contextNotice = t('ai.compactionFailed')
  store.contextStats = { beforeTokens: inputTokens, afterTokens: outputTokens, durationMs: Math.round(performance.now() - started), fallback: failed }
  return { messages, failed }
}
