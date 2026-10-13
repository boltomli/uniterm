// Tab-bar overflow helper for the DB / Mongo tab strips.
//
// The "more" dropdown button must reflect whether the tab strip's content is
// wider than the visible container, and it must keep reacting to resizes.
// The scroll element sits behind v-if and only exists while at least one tab
// is open, so a ResizeObserver attached once in onMounted never observed
// anything when the component mounted without tabs (security.md finding 11).
//
// This composable watches the template ref itself: it observes the element
// when it appears, unobserves it when it goes away, re-measures on every
// observer callback, and disconnects when the owning effect scope stops
// (component setup runs inside the component's scope, so that is unmount).
import { onScopeDispose, ref, watch, type Ref } from 'vue'

export interface TabBarOverflow {
  /** True while the strip's content is wider than the visible container. */
  showMore: Ref<boolean>
  /** Re-measure the current element (after the tab list or active tab changed). */
  measure: () => void
}

export function useTabBarOverflow(scrollRef: Ref<HTMLElement | null>): TabBarOverflow {
  const showMore = ref(false)

  function measure(): void {
    const el = scrollRef.value
    if (!el) return
    showMore.value = el.scrollWidth > el.clientWidth + 1
  }

  let observer: ResizeObserver | null = null
  let observed: HTMLElement | null = null

  function detach(): void {
    if (!observer || !observed) return
    observer.unobserve(observed)
    observed = null
  }

  const stopWatch = watch(
    scrollRef,
    el => {
      if (!el) {
        detach()
        return
      }
      if (observed === el) return
      detach()
      observer ??= new ResizeObserver(measure)
      observer.observe(el)
      observed = el
      measure()
    },
    { flush: 'post', immediate: true },
  )

  onScopeDispose(() => {
    stopWatch()
    observer?.disconnect()
    observer = null
    observed = null
  })

  return { showMore, measure }
}
