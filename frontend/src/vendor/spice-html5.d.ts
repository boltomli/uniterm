// Type declarations for the vendored spice-html5 library (plain JS with no
// upstream types). Declares only the surface this app uses: the connection
// constructor and the option fields passed to it.
export interface SpiceMainConnOptions {
  uri: string
  password?: string
  screen_id?: string
  onerror?: (e: unknown) => void
  onsuccess?: (m: unknown) => void
}
export class SpiceMainConn {
  constructor(options: SpiceMainConnOptions)
  stop(): void
}
