// @novnc/novnc ships JavaScript without type declarations. This declares the
// subset of the RFB API the VNC tab uses; the app loads the class through
// dynamic import and stashes it on window, so members are consumed loosely.
declare module '@novnc/novnc' {
  export interface RFBEvent {
    detail?: unknown
    [key: string]: unknown
  }

  export class RFB {
    constructor(target: HTMLElement, url: string, options?: Record<string, unknown>)
    addEventListener(type: string, listener: (event: RFBEvent) => void): void
    removeEventListener(type: string, listener: (event: RFBEvent) => void): void
    disconnect(): void
    focus(): void
    blur(): void
    clipboardPasteFrom(text: string): void
    sendCredentials(credentials: Record<string, string>): void
    viewOnly: boolean
    scaleViewport: boolean
    resizeSession: boolean
    showDotCursor: boolean
    background: string
    clipViewport: boolean
    compressionLevel: number
    qualityLevel: number
    machineType?: string
    _rfbMaxVersion?: number
  }

  export default RFB
}
