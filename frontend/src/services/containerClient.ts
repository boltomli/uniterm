import {
  ContainerConnect, ContainerDisconnect, ContainerList, ContainerInspect,
  ContainerAction, ContainerRename, ContainerStats, ContainerImages,
  ContainerRemoveImage, ContainerCreate, ContainerNamespaces,
  ContainerSetNamespace, ContainerStartLogs, ContainerStartPull,
  ContainerStartPush, ContainerTagImage, ContainerImagePrune,
  ContainerImageInspect, ContainerImageHistory, ContainerRegistryLogin,
  ContainerRuntimeInfo, ContainerStopStream, ContainerExecSession,
} from '../../bindings/github.com/ys-ll/uniterm/app'
import { Events } from '@wailsio/runtime'
import type {
  ContainerInfo, InspectResult, ContainerImage, ContainerStats as ContainerStatsInfo, ContainerCreateOptions,
  ContainerTransferOptions, ContainerLoginOptions, ImageLayer, RuntimeInfo,
} from '../types/container'

// 凭据补全弹窗（useTunnelCredentials）提交的临时账密；error 非 null 表示用户取消，
// store 据此展示取消状态而不发起连接。
export interface ContainerConnectCreds {
  sshUser?: string
  sshPassword?: string
  tunnelUser?: string
  tunnelPassword?: string
  error?: string
}

export const connect = (id: string, creds?: ContainerConnectCreds) =>
  ContainerConnect(id, creds?.sshUser || '', creds?.sshPassword || '', creds?.tunnelUser || '', creds?.tunnelPassword || '')
export const disconnect = (id: string) => ContainerDisconnect(id)
export const list = (id: string) => ContainerList(id) as Promise<ContainerInfo[]>
export const inspect = (id: string, cid: string) => ContainerInspect(id, cid) as Promise<InspectResult>
export const action = (id: string, cid: string, act: string) => ContainerAction(id, cid, act)
export const rename = (id: string, cid: string, name: string) => ContainerRename(id, cid, name)
export const stats = (id: string) => ContainerStats(id) as Promise<ContainerStatsInfo[]>
export const images = (id: string) => ContainerImages(id) as Promise<ContainerImage[]>
export const removeImage = (id: string, imageID: string) => ContainerRemoveImage(id, imageID)
export const create = (id: string, opts: ContainerCreateOptions) => ContainerCreate(id, opts as any)
export const namespaces = (id: string) => ContainerNamespaces(id) as Promise<string[]>
export const setNamespace = (connId: string, ns: string) => ContainerSetNamespace(connId, ns)
export const execSession = (connId: string, cid: string, shell: string) =>
  ContainerExecSession(connId, cid, shell)

export interface StreamHandle {
  id: string
  stop: () => void
}

async function startStream(
  start: () => Promise<string>,
  onLine: (line: string) => void,
  onEnd?: (err: string) => void
): Promise<StreamHandle> {
  const id = await start()
  const evName = `container:stream:${id}`
  const endName = `container:stream-end:${id}`
 Events.On(evName, (ev) => { const p: { line: string } = ev.data; return onLine(p?.line ?? '') })
 Events.On(endName, (ev) => { const p: { error: string } = ev.data; 
    onEnd?.(p?.error || '')
    Events.Off(evName)
    Events.Off(endName)
   })
  return {
    id,
    stop: () => {
      Events.Off(evName)
      Events.Off(endName)
      ContainerStopStream(id)
    },
  }
}

export const startLogs = (connId: string, cid: string, tail: number, timestamps: boolean,
  onLine: (l: string) => void, onEnd?: (e: string) => void) =>
  startStream(() => ContainerStartLogs(connId, cid, tail, timestamps), onLine, onEnd)

export const startPull = (connId: string, image: string, opts: ContainerTransferOptions,
  onLine: (l: string) => void, onEnd?: (e: string) => void) =>
  startStream(() => ContainerStartPull(connId, image, opts as any), onLine, onEnd)

export const startPush = (connId: string, image: string, opts: ContainerTransferOptions,
  onLine: (l: string) => void, onEnd?: (e: string) => void) =>
  startStream(() => ContainerStartPush(connId, image, opts as any), onLine, onEnd)

export const tagImage = (connId: string, image: string, repoTag: string) =>
  ContainerTagImage(connId, image, repoTag)

export const imagePrune = (connId: string) => ContainerImagePrune(connId)

export const imageInspect = (connId: string, imageID: string) =>
  ContainerImageInspect(connId, imageID) as Promise<string>

export const imageHistory = (connId: string, imageID: string) =>
  ContainerImageHistory(connId, imageID) as Promise<ImageLayer[]>

export const runtimeInfo = (connId: string) =>
  ContainerRuntimeInfo(connId) as Promise<RuntimeInfo>

export const registryLogin = (connId: string, opts: ContainerLoginOptions) =>
  ContainerRegistryLogin(connId, opts as any)
