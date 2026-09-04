import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import '@fontsource-variable/jetbrains-mono'
import { ElDialog } from 'element-plus'
import App from './App.vue'
import './style.css'
import { Window } from '@wailsio/runtime'
import { useSettingsStore } from './stores/settingsStore'
import { setLocale } from './i18n'

Window.SetTitle('uniTerm')

// Set ElDialog draggable by default
if (ElDialog.props) {
  ElDialog.props.draggable = { type: Boolean, default: true }
}

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(ElementPlus)

const settingsStore = useSettingsStore()
// Sync locale from navigator before mount (default 'system' → resolves from
// navigator.language, zero IPC) so the first paint is already in the user's
// language; init() re-resolves with the persisted preference once loaded.
setLocale('system')
// Fire settings init without awaiting: top-level await here used to block the
// module's completion, which delayed the page load event, which delayed Wails'
// window Show() — seconds of dock-icon-but-no-window on slow first paint. All
// settings consumers are reactive (computed/watch/template), so values apply
// themselves once init lands. applyTheme() on the loaded defaults runs before
// mount anyway (init() calls it synchronously after its awaits settle).
settingsStore.init()

app.mount('#app')

// Global context menu closer: broadcast to all menu components via window event
document.addEventListener('contextmenu', () => {
  window.dispatchEvent(new CustomEvent('global:close-context-menus'))
}, true)

document.addEventListener('contextmenu', (e) => {
  const target = e.target as HTMLElement
  // Read-only log-path toast: offer copy/select-all on its plain text.
  const copyable = target.closest('.msg-copyable') as HTMLElement | null
  if (copyable) {
    e.preventDefault()
    const content = (copyable.querySelector('.el-message__content') as HTMLElement) || copyable
    window.dispatchEvent(new CustomEvent('input:contextmenu', {
      detail: { x: e.clientX, y: e.clientY, target: content, readonly: true }
    }))
    return
  }
  const tag = target.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || target.isContentEditable) {
    e.preventDefault()
    window.dispatchEvent(new CustomEvent('input:contextmenu', {
      detail: { x: e.clientX, y: e.clientY, target }
    }))
    return
  }
  e.preventDefault()
})
