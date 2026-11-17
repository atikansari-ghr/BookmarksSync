import '@testing-library/jest-dom/vitest'
import { afterEach, beforeEach, expect, vi } from 'vitest'
import { cleanup } from '@testing-library/react'

// The UI must never put anything (least of all auth secrets) into browser storage.
let storageCalls: string[] = []

beforeEach(() => {
  storageCalls = []
  for (const method of ['setItem', 'getItem', 'removeItem', 'clear', 'key'] as const) {
    vi.spyOn(Storage.prototype, method).mockImplementation(function (this: Storage, ...args: unknown[]) {
      storageCalls.push(`${method}(${args.map(String).join(',')})`)
      return null as never
    })
  }
})

afterEach(() => {
  cleanup()
  expect(storageCalls, 'browser storage must not be used').toEqual([])
})
