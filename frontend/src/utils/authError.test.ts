import { describe, expect, it } from 'vitest'
import { isAuthFailureError } from './authError'

describe('isAuthFailureError', () => {
  it('matches x/crypto auth exhaustion and server rejections', () => {
    expect(isAuthFailureError('ssh handshake: ssh: unable to authenticate, attempted methods [password], no supported methods remain')).toBe(true)
    expect(isAuthFailureError('Permission denied (publickey,password)')).toBe(true)
    expect(isAuthFailureError('Permission denied, please try again.')).toBe(true)
  })

  it('is case-insensitive', () => {
    expect(isAuthFailureError('UNABLE TO AUTHENTICATE')).toBe(true)
  })

  it('rejects non-auth failures', () => {
    expect(isAuthFailureError('tcp dial: dial tcp 10.0.0.1:22: connectex: No connection could be made because the target machine actively refused it.')).toBe(false)
    expect(isAuthFailureError('ssh handshake: ssh: handshake failed: EOF')).toBe(false)
    expect(isAuthFailureError('auth timeout')).toBe(false)
    expect(isAuthFailureError('auth cancelled')).toBe(false)
    expect(isAuthFailureError('tunnel start: ssh: handshake failed: ssh: unable to authenticate')).toBe(true)
    expect(isAuthFailureError('')).toBe(false)
    expect(isAuthFailureError(undefined)).toBe(false)
  })
})
