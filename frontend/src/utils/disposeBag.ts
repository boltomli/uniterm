// One-shot teardown bag for per-component resources that outlive their owner.
//
// BaseTerminal loads addons onto the SHARED xterm instance (which survives
// remounts) and subscribes to shared addon events; anything not released on
// unmount stacks up on every remount (security.md component chapter #13,
// #22). The bag collects those releasables so the unmount hook releases
// everything in one call, most recent first.

export interface Disposable {
  dispose(): void
}

export interface DisposeBag {
  /** Track an xterm-style disposable; returned unchanged for inline use. */
  addDisposable<T extends Disposable>(item: T): T
  /** Track a pending timeout so teardown clears it. Generic over the handle
   *  type: `setTimeout` is `number` under the DOM lib and `Timeout` under the
   *  Node types, and both appear across the app's files. */
  addTimeout<T extends Parameters<typeof clearTimeout>[0]>(id: T): T
  /** Track a raw release callback (unsubscribe functions and friends). */
  add(release: () => void): void
  /** Release everything, most recent first. Idempotent. */
  disposeAll(): void
  readonly size: number
}

export function createDisposeBag(): DisposeBag {
  const releases: (() => void)[] = []

  return {
    addDisposable(item) {
      releases.push(() => item.dispose())
      return item
    },

    addTimeout(id) {
      releases.push(() => clearTimeout(id))
      return id
    },

    add(release) {
      releases.push(release)
    },

    disposeAll() {
      while (releases.length) {
        const release = releases.pop()!
        // Teardown must continue past a failing release: one addon throwing
        // in dispose() may not leak everything still queued behind it.
        try {
          release()
        } catch {
          /* best-effort teardown */
        }
      }
    },

    get size() {
      return releases.length
    },
  }
}
