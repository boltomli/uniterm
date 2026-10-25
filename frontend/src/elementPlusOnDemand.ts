// Element Plus is imported on demand (see vite.config.ts:
// unplugin-vue-components + ElementPlusResolver), which only injects a
// component's stylesheet for components used in a *template*. Two other
// usage patterns therefore get no CSS and are covered here instead:
//
//  1. components imported inside <script> — ElMessageBox.confirm() and
//     friends, plus the handful of components referenced by name in script
//     (ElButton, ElInput, ...);
//  2. programmatic APIs — ElMessage()/ElMessageBox() calls spread across
//     ~40 modules.
//
// Keep this list in sync with `grep -rn "from 'element-plus'" src` when
// adding a new script-side import. Every entry resolves to
// element-plus/es/components/<name>/style/css, which also pulls in the
// component's own dependencies (base, popper, ...), so nothing else has to
// be listed. Without these the UI still works but renders unstyled.
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'
import 'element-plus/es/components/button/style/css'
import 'element-plus/es/components/checkbox/style/css'
import 'element-plus/es/components/dialog/style/css'
import 'element-plus/es/components/icon/style/css'
import 'element-plus/es/components/input/style/css'
import 'element-plus/es/components/option/style/css'
import 'element-plus/es/components/select/style/css'
