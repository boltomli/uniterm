<template>
  <div ref="hostRef" class="syntax-editor" :class="{ compact, 'theme-dark': isDark }" />
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { syntaxHighlighting, defaultHighlightStyle, bracketMatching, foldGutter, foldKeymap, StreamLanguage } from '@codemirror/language'
import { oneDark } from '@codemirror/theme-one-dark'
import { json } from '@codemirror/lang-json'
import { javascript } from '@codemirror/lang-javascript'
import { html } from '@codemirror/lang-html'
import { css } from '@codemirror/lang-css'
import { xml } from '@codemirror/lang-xml'
import { markdown } from '@codemirror/lang-markdown'
import { python } from '@codemirror/lang-python'
import { sql } from '@codemirror/lang-sql'
import { yaml } from '@codemirror/lang-yaml'
import { shell } from '@codemirror/legacy-modes/mode/shell'
import { properties } from '@codemirror/legacy-modes/mode/properties'
import { toml } from '@codemirror/legacy-modes/mode/toml'
import { dockerFile } from '@codemirror/legacy-modes/mode/dockerfile'
import { nginx } from '@codemirror/legacy-modes/mode/nginx'
import { clike } from '@codemirror/legacy-modes/mode/clike'
import { go } from '@codemirror/legacy-modes/mode/go'
import { rust } from '@codemirror/legacy-modes/mode/rust'
import { ruby } from '@codemirror/legacy-modes/mode/ruby'
import { useSettingsStore } from '../stores/settingsStore'

const props = defineProps<{
  modelValue: string
  filePath?: string
  compact?: boolean
  lang?: string
  wrap?: boolean
  readonly?: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  execute: []
}>()

const hostRef = ref<HTMLElement | null>(null)
let view: EditorView | null = null
let applyingExternal = false
let resizeObserver: ResizeObserver | null = null

// Follow the app theme (dark/deep-blue → dark, light/system → resolved).
const settingsStore = useSettingsStore()
const isDark = computed(() => settingsStore.resolvedAppTheme === 'dark')

function extOf(path: string): string {
  const base = path.replace(/\\/g, '/').split('/').pop() || ''
  const lower = base.toLowerCase()
  if (lower === 'dockerfile' || lower.startsWith('dockerfile.')) return 'dockerfile'
  if (lower === 'makefile' || lower === 'gnumakefile') return 'makefile'
  if (lower === '.bashrc' || lower === '.zshrc' || lower === '.profile' || lower === '.bash_profile') return 'sh'
  if (lower === '.gitignore' || lower === '.dockerignore' || lower === '.env' || lower.endsWith('.env')) return 'conf'
  if (lower === 'nginx.conf' || lower.endsWith('.nginx')) return 'nginx'
  const i = lower.lastIndexOf('.')
  return i >= 0 ? lower.slice(i + 1) : ''
}

// Force a specific highlight mode; matches the extension-based picks. 'text' or
// anything unknown yields no language extension (plain text).
function languageFor(id?: string) {
  switch (id) {
    case 'json': return json()
    case 'js': return javascript()
    case 'ts': return javascript({ typescript: true })
    case 'html': return html()
    case 'css': return css()
    case 'xml': return xml()
    case 'md': return markdown()
    case 'py': return python()
    case 'sql': return sql()
    case 'yaml': return yaml()
    case 'sh': return StreamLanguage.define(shell)
    case 'conf': return StreamLanguage.define(properties)
    case 'toml': return StreamLanguage.define(toml)
    case 'dockerfile': return StreamLanguage.define(dockerFile)
    case 'nginx': return StreamLanguage.define(nginx)
    case 'c': return StreamLanguage.define((clike as any).c)
    case 'cpp': return StreamLanguage.define((clike as any).cpp)
    case 'csharp': return StreamLanguage.define((clike as any).csharp)
    case 'dart': return StreamLanguage.define((clike as any).dart)
    case 'java': return StreamLanguage.define((clike as any).java)
    case 'kotlin': return StreamLanguage.define((clike as any).kotlin)
    case 'scala': return StreamLanguage.define((clike as any).scala)
    case 'go': return StreamLanguage.define(go)
    case 'rust': return StreamLanguage.define(rust)
    case 'ruby': return StreamLanguage.define(ruby)
    default: return []
  }
}

function languageExtension(path: string) {
  const ext = extOf(path || '')
  switch (ext) {
    case 'json':
    case 'jsonc':
    case 'json5':
      return json()
    case 'js':
    case 'mjs':
    case 'cjs':
    case 'jsx':
    case 'ts':
    case 'tsx':
      return javascript({ typescript: ext === 'ts' || ext === 'tsx', jsx: ext === 'jsx' || ext === 'tsx' })
    case 'html':
    case 'htm':
    case 'vue':
      return html()
    case 'css':
    case 'scss':
    case 'less':
      return css()
    case 'xml':
    case 'svg':
    case 'plist':
      return xml()
    case 'md':
    case 'markdown':
      return markdown()
    case 'py':
      return python()
    case 'sql':
      return sql()
    case 'yml':
    case 'yaml':
      return yaml()
    case 'sh':
    case 'bash':
    case 'zsh':
    case 'ksh':
    case 'fish':
      return StreamLanguage.define(shell)
    case 'conf':
    case 'cfg':
    case 'ini':
    case 'properties':
    case 'service':
    case 'desktop':
      return StreamLanguage.define(properties)
    case 'toml':
      return StreamLanguage.define(toml)
    case 'dockerfile':
      return StreamLanguage.define(dockerFile)
    case 'nginx':
      return StreamLanguage.define(nginx)
    default:
      if (/\.conf(\.|$)/i.test(path || '') || /\/conf\//i.test(path || '')) {
        return StreamLanguage.define(properties)
      }
      return []
  }
}

function buildExtensions(path: string) {
  return [
    lineNumbers(),
    highlightActiveLine(),
    highlightActiveLineGutter(),
    drawSelection(),
    history(),
    foldGutter(),
    bracketMatching(),
    // Dark: One Dark tokens; light: defaultHighlightStyle (light-tuned) takes
    // over as the active highlighter via the fallback chain.
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    ...(isDark.value ? [oneDark] : []),
    EditorState.readOnly.of(!!props.readonly),
    EditorView.editable.of(!props.readonly),
    props.lang ? languageFor(props.lang) : languageExtension(path),
    keymap.of([
      {
        key: 'Mod-Enter',
        run: () => {
          emit('execute')
          return true
        },
      },
      ...defaultKeymap,
      ...historyKeymap,
      ...foldKeymap,
      indentWithTab,
    ]),
    EditorView.updateListener.of((update) => {
      if (!update.docChanged || applyingExternal) return
      emit('update:modelValue', update.state.doc.toString())
    }),
    EditorView.theme({
      '&': {
        height: '100%',
        width: '100%',
        maxWidth: '100%',
        fontSize: '13px',
        outline: 'none',
      },
      '&.cm-editor': {
        height: '100%',
        width: '100%',
      },
      '&.cm-editor.cm-focused': { outline: 'none' },
      '.cm-scroller': {
        fontFamily: 'var(--font-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace)',
        overflow: 'auto',
        width: '100%',
        minWidth: '0',
      },
      '.cm-content': { minHeight: '100%', width: '100%', boxSizing: 'border-box' },
      '.cm-gutters': { backgroundColor: 'transparent', border: 'none' },
      // Strong selection highlight — oneDark default is too subtle, and has-bg
      // mode can further wash it out via global transparent rules.
      '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': {
        backgroundColor: 'rgba(64, 158, 255, 0.45) !important',
      },
      '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground': {
        backgroundColor: 'rgba(64, 158, 255, 0.45) !important',
      },
      '.cm-content ::selection': {
        backgroundColor: 'rgba(64, 158, 255, 0.45) !important',
      },
    }),
    ...(props.wrap === false ? [] : [EditorView.lineWrapping]),
  ]
}

function requestMeasure() {
  view?.requestMeasure()
}

function createEditor() {
  if (!hostRef.value) return
  view?.destroy()
  view = new EditorView({
    parent: hostRef.value,
    state: EditorState.create({
      doc: props.modelValue || '',
      extensions: buildExtensions(props.filePath || ''),
    }),
  })
  requestMeasure()
}

function setDoc(text: string) {
  if (!view) return
  const cur = view.state.doc.toString()
  if (cur === text) return
  applyingExternal = true
  view.dispatch({
    changes: { from: 0, to: view.state.doc.length, insert: text },
  })
  applyingExternal = false
}

watch(() => props.modelValue, (v) => {
  setDoc(v ?? '')
})

watch(() => [props.filePath, props.lang, props.wrap, props.readonly], async () => {
  const text = view?.state.doc.toString() ?? props.modelValue
  await nextTick()
  createEditor()
  if (text !== props.modelValue) setDoc(text)
})

// Theme swap needs a full rebuild: oneDark vs defaultHighlightStyle are
// different extension sets, and EditorState extensions are immutable.
watch(isDark, async () => {
  const text = view?.state.doc.toString() ?? props.modelValue
  await nextTick()
  createEditor()
  if (text !== props.modelValue) setDoc(text)
})

onMounted(() => {
  createEditor()
  if (hostRef.value && typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(() => requestMeasure())
    resizeObserver.observe(hostRef.value)
  }
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  view?.destroy()
  view = null
})

defineExpose({
  focus: () => view?.focus(),
  getValue: () => view?.state.doc.toString() ?? props.modelValue,
  getSelectedOrAll: () => {
    if (!view) return props.modelValue
    const { from, to } = view.state.selection.main
    if (from !== to) return view.state.sliceDoc(from, to)
    return view.state.doc.toString()
  },
})
</script>

<style scoped>
.syntax-editor {
  position: relative;
  width: 100%;
  min-width: 0;
  height: 55vh;
  border: 1px solid var(--border-subtle);
  border-radius: 4px;
  overflow: hidden;
  background: var(--bg-base);
  color: var(--text-primary);
  box-sizing: border-box;
}
/* Keep the container background in sync with the active CodeMirror theme. */
.syntax-editor.theme-dark {
  background: #282c34;
  color: #abb2bf;
}
.syntax-editor.compact {
  height: 100%;
  width: 100%;
  min-width: 0;
  flex: 1 1 auto;
  align-self: stretch;
  border-radius: var(--radius-sm);
}
.syntax-editor :deep(.cm-editor) {
  position: absolute !important;
  inset: 0 !important;
  width: auto !important;
  height: auto !important;
  max-width: none !important;
}
.syntax-editor :deep(.cm-scroller) {
  min-width: 0 !important;
}
.syntax-editor :deep(.cm-focused) {
  outline: none;
}
.syntax-editor :deep(.cm-selectionBackground),
.syntax-editor :deep(.cm-focused .cm-selectionBackground),
.syntax-editor :deep(.cm-content ::selection) {
  background-color: rgba(64, 158, 255, 0.45) !important;
}
</style>
