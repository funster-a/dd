// Чтение QR с камеры. Где есть встроенный BarcodeDetector (Chrome, Android)
// — он, иначе jsQR по кадрам с canvas (Safari, Firefox). jsQR грузится
// только при необходимости.

interface DetectedBarcode {
  rawValue: string
}
interface BarcodeDetectorLike {
  detect(source: CanvasImageSource): Promise<DetectedBarcode[]>
}
declare global {
  interface Window {
    BarcodeDetector?: {
      new (opts: { formats: string[] }): BarcodeDetectorLike
      getSupportedFormats(): Promise<string[]>
    }
  }
}

export interface QrReader {
  stop(): void
}

export class CameraError extends Error {
  constructor(
    readonly reason: 'denied' | 'missing' | 'insecure' | 'other',
    message: string,
  ) {
    super(message)
  }
}

async function detector(): Promise<(v: HTMLVideoElement, c: HTMLCanvasElement) => Promise<string | null>> {
  if (window.BarcodeDetector) {
    try {
      const formats = await window.BarcodeDetector.getSupportedFormats()
      if (formats.includes('qr_code')) {
        const d = new window.BarcodeDetector({ formats: ['qr_code'] })
        return async (v) => (await d.detect(v))[0]?.rawValue ?? null
      }
    } catch {
      // падаем на jsQR
    }
  }
  const { default: jsQR } = await import('jsqr')
  return async (v, c) => {
    const w = v.videoWidth
    const h = v.videoHeight
    if (!w || !h) return null
    // Центральный квадрат кадра, уменьшенный: быстрее и достаточно для QR.
    const side = Math.min(w, h)
    const size = Math.min(side, 640)
    c.width = size
    c.height = size
    const ctx = c.getContext('2d', { willReadFrequently: true })
    if (!ctx) return null
    ctx.drawImage(v, (w - side) / 2, (h - side) / 2, side, side, 0, 0, size, size)
    const img = ctx.getImageData(0, 0, size, size)
    return jsQR(img.data, size, size, { inversionAttempts: 'dontInvert' })?.data ?? null
  }
}

// startReader включает заднюю камеру в video и вызывает onCode на каждый
// распознанный код (не чаще ~8 раз в секунду).
export async function startReader(video: HTMLVideoElement, onCode: (code: string) => void): Promise<QrReader> {
  if (!window.isSecureContext) throw new CameraError('insecure', 'Камера работает только по HTTPS')
  if (!navigator.mediaDevices?.getUserMedia) throw new CameraError('missing', 'Браузер не даёт доступ к камере')
  let stream: MediaStream
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      video: { facingMode: { ideal: 'environment' }, width: { ideal: 1280 }, height: { ideal: 720 } },
      audio: false,
    })
  } catch (e) {
    const name = (e as DOMException).name
    if (name === 'NotAllowedError' || name === 'SecurityError') throw new CameraError('denied', 'Доступ к камере запрещён')
    if (name === 'NotFoundError' || name === 'OverconstrainedError') throw new CameraError('missing', 'Камера не найдена')
    throw new CameraError('other', 'Не удалось включить камеру')
  }
  video.srcObject = stream
  video.setAttribute('playsinline', '')
  video.muted = true
  await video.play().catch(() => {})

  const read = await detector()
  const canvas = document.createElement('canvas')
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined

  const tick = async () => {
    if (stopped) return
    try {
      if (video.readyState >= 2) {
        const code = await read(video, canvas)
        if (code && !stopped) onCode(code)
      }
    } catch {
      // кадр не распознан — пробуем следующий
    }
    timer = setTimeout(tick, 120)
  }
  tick()

  return {
    stop() {
      stopped = true
      clearTimeout(timer)
      stream.getTracks().forEach((t) => t.stop())
      video.srcObject = null
    },
  }
}
