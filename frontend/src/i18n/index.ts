import { computed, ref, shallowRef } from 'vue'
import type { Language, Locale } from '../types/settings'
import { SUPPORTED_LOCALES } from '../types/settings'
import type { Language as ElementPlusLanguage } from 'element-plus/es/locale'
// English is bundled, not fetched: it is the fallback table for every other
// locale, and code that runs before the first locale chunk lands (startup
// error strings in backendError/llm, store initialisation) must produce
// readable English instead of raw `some.key` placeholders. The other ten
// tables are ~1.1 MB of JSON — bundling those was most of what made the
// first-load bundle so large.
import en from './locales/en.json'

type MessageTable = Record<string, string>

// Every other locale is its own chunk, fetched on demand.
const localeLoaders: Record<Locale, () => Promise<{ default: MessageTable }>> = {
  'zh-CN': () => import('./locales/zh-CN.json'),
  'zh-TW': () => import('./locales/zh-TW.json'),
  en: () => import('./locales/en.json'),
  ja: () => import('./locales/ja.json'),
  ko: () => import('./locales/ko.json'),
  de: () => import('./locales/de.json'),
  es: () => import('./locales/es.json'),
  fr: () => import('./locales/fr.json'),
  ru: () => import('./locales/ru.json'),
  pt: () => import('./locales/pt.json'),
  vi: () => import('./locales/vi.json'),
}

// element-plus ships its own translation tables (date pickers, pagination,
// selects...). They are loaded together with the matching app locale so
// el-config-provider never shows English UI text after the app language is
// already applied. Loaded in the same round-trip as the table above, so
// awaiting setLocale() covers both.
const epLocaleLoaders: Record<Locale, () => Promise<{ default: ElementPlusLanguage }>> = {
  'zh-CN': () => import('element-plus/es/locale/lang/zh-cn'),
  'zh-TW': () => import('element-plus/es/locale/lang/zh-tw'),
  en: () => import('element-plus/es/locale/lang/en'),
  ja: () => import('element-plus/es/locale/lang/ja'),
  ko: () => import('element-plus/es/locale/lang/ko'),
  de: () => import('element-plus/es/locale/lang/de'),
  es: () => import('element-plus/es/locale/lang/es'),
  fr: () => import('element-plus/es/locale/lang/fr'),
  ru: () => import('element-plus/es/locale/lang/ru'),
  pt: () => import('element-plus/es/locale/lang/pt'),
  vi: () => import('element-plus/es/locale/lang/vi'),
}

// Loaded tables only. Replaced (not mutated) on every load so every t()
// consumer re-renders when a table lands — that is what makes a runtime
// language switch apply without a reload. English starts out loaded.
const messages = shallowRef<Partial<Record<Locale, MessageTable>>>({ en })

// The element-plus table for the *current* locale; App.vue feeds it straight
// into <el-config-provider :locale>. null until the first load completes.
export const elementPlusLocale = shallowRef<ElementPlusLanguage | null>(null)

const currentLocale = ref<Locale>('en')

export const locale = computed(() => currentLocale.value)

const localePrefixMap: Record<string, Locale> = Object.fromEntries(
  SUPPORTED_LOCALES.filter((loc) => loc !== 'zh-CN' && loc !== 'zh-TW').map((loc) => [loc, loc])
)

function resolveLocale(lang: Language): Locale {
  if (lang !== 'system') {
    if (SUPPORTED_LOCALES.includes(lang as Locale)) return lang as Locale
    return 'en'
  }

  if (typeof navigator === 'undefined' || !navigator.language) return 'en'

  const nav = navigator.language.toLowerCase()

  if (nav.startsWith('zh')) {
    if (nav.includes('tw') || nav.includes('hk') || nav.includes('mo')) return 'zh-TW'
    return 'zh-CN'
  }

  for (const [prefix, loc] of Object.entries(localePrefixMap)) {
    if (nav.startsWith(prefix)) return loc
  }

  return 'en'
}

const loads = new Map<Locale, Promise<void>>()

// Fetches (once) the app table and the element-plus table for `loc`. A
// failure leaves the locale unloaded: t() then falls back to English and
// finally to the key, which is the same degradation a missing key has —
// the promise itself never rejects so callers can await it unguarded.
function loadLocale(loc: Locale): Promise<void> {
  const inFlight = loads.get(loc)
  if (inFlight) return inFlight

  const load = (async () => {
    try {
      const [msgs, ep] = await Promise.all([localeLoaders[loc](), epLocaleLoaders[loc]()])
      messages.value = { ...messages.value, [loc]: msgs.default }
      // A slower load for a locale the user already switched away from must
      // not overwrite the table of the locale they switched to.
      if (currentLocale.value === loc) elementPlusLocale.value = ep.default
    } catch {
      // Handled by the fallbacks in translate().
    }
  })()

  loads.set(loc, load)
  return load
}

// Switches the UI language and returns a promise that settles once both the
// app table and the element-plus table for the new language are in memory.
// main.ts awaits it before mounting so the first paint is already in the
// user's language; later calls (settings changes) are fire-and-forget and
// apply reactively when the chunk lands.
export function setLocale(lang: Language): Promise<void> {
  const loc = resolveLocale(lang)
  currentLocale.value = loc
  return loadLocale(loc)
}

function translate(loc: Locale, key: string, params?: Record<string, string | number>): string {
  const table = messages.value[loc] ?? messages.value['en']
  const msg = table?.[key] ?? messages.value['en']?.[key] ?? key
  if (!params) return msg
  return msg.replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? `{${k}}`))
}

export function useI18n() {
  function t(key: string, params?: Record<string, string | number>): string {
    return translate(currentLocale.value, key, params)
  }
  return { t, locale }
}

export function t(key: string, params?: Record<string, string | number>): string {
  return translate(currentLocale.value, key, params)
}
