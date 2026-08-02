import { describe, it, expect, vi, beforeAll } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef } from 'react'
import { SignaturePad, type SignaturePadHandle, strokesToSVGPath } from '@/components/signatures/SignaturePad'

// jsdom has no canvas 2d context; stub just enough for the pad to mount.
beforeAll(() => {
  // jsdom ships no canvas backend; a partial stub is all the pad needs.
  HTMLCanvasElement.prototype.getContext = (() => ({
    scale: vi.fn(), clearRect: vi.fn(), beginPath: vi.fn(), moveTo: vi.fn(),
    lineTo: vi.fn(), stroke: vi.fn(), drawImage: vi.fn(),
    lineCap: 'round', lineJoin: 'round', lineWidth: 0, strokeStyle: '',
  })) as unknown as HTMLCanvasElement['getContext']
})

describe('<SignaturePad> image upload', () => {
  it('offers an upload affordance alongside drawing', () => {
    render(<SignaturePad />)
    expect(screen.getByTestId('signature-pad-upload')).toBeInTheDocument()
    expect(screen.getByText(/upload an image/i)).toBeInTheDocument()
  })

  it('rejects a non-image file without marking the pad filled', async () => {
    const user = userEvent.setup()
    const ref = createRef<SignaturePadHandle>()
    render(<SignaturePad ref={ref} />)
    const input = screen.getByTestId('signature-pad-file') as HTMLInputElement
    await user.upload(input, new File(['nope'], 'a.txt', { type: 'text/plain' }))
    expect(ref.current?.isEmpty()).toBe(true)
  })

  it('submit stays disabled until there is a signature', () => {
    render(<SignaturePad />)
    expect(screen.getByTestId('signature-pad-submit')).toBeDisabled()
    expect(screen.getByTestId('signature-pad-clear')).toBeDisabled()
  })
})

describe('strokesToSVGPath', () => {
  it('emits one M/L segment per stroke', () => {
    const d = strokesToSVGPath([{ points: [{ x: 1, y: 2 }, { x: 3, y: 4 }] }])
    expect(d).toContain('M1')
    expect(d).toContain('L3')
  })
  it('returns empty string with no strokes', () => {
    expect(strokesToSVGPath([])).toBe('')
  })
})
