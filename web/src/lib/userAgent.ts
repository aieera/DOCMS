// Lightweight UA classifier — pulls a readable device + browser pair
// out of a User-Agent string without adding a library dep. The input
// is the raw `user_agent` value the backend persists on
// upload_sessions / sessions rows.
//
// We cover the platforms that make up ~99% of our audit data. Unknown
// UAs fall back to "Unknown device" so the UI never renders "null".

export interface UADescription {
  device: string
  browser: string
  os: string
}

export function parseUserAgent(raw: string): UADescription {
  const ua = (raw || '').trim()
  if (!ua) return { device: 'Unknown device', browser: '', os: '' }

  const os = detectOS(ua)
  const browser = detectBrowser(ua)
  const device = detectDevice(ua, os)
  return { device, browser, os }
}

function detectOS(ua: string): string {
  if (/Windows NT 10\.0/.test(ua)) return 'Windows 10/11'
  if (/Windows NT/.test(ua)) return 'Windows'
  if (/Mac OS X/.test(ua)) return 'macOS'
  if (/Android/.test(ua)) return 'Android'
  if (/iPhone|iPad|iPod/.test(ua)) return 'iOS'
  if (/Linux/.test(ua)) return 'Linux'
  return ''
}

function detectBrowser(ua: string): string {
  // Order matters — Edge reports as Chrome + Edg, Chrome reports as
  // Safari + Chrome. Check the most specific first.
  if (/Edg\//.test(ua)) return 'Edge'
  if (/OPR\/|Opera/.test(ua)) return 'Opera'
  if (/Firefox\//.test(ua)) return 'Firefox'
  if (/Chrome\//.test(ua)) return 'Chrome'
  if (/Safari\//.test(ua)) return 'Safari'
  if (/curl\//i.test(ua)) return 'curl'
  if (/PostmanRuntime/i.test(ua)) return 'Postman'
  return 'Browser'
}

function detectDevice(ua: string, os: string): string {
  if (/iPhone/.test(ua)) return 'iPhone'
  if (/iPad/.test(ua)) return 'iPad'
  if (/Android.*Mobile/.test(ua)) return 'Android phone'
  if (/Android/.test(ua)) return 'Android tablet'
  if (os === 'macOS') return 'Mac'
  if (os === 'Windows 10/11' || os === 'Windows') return 'Windows PC'
  if (os === 'Linux') return 'Linux'
  return 'Desktop'
}

// FormatDeviceLine returns a one-line "iPhone · Safari · iOS" string.
// Empty segments are dropped so we don't render " · · ".
export function formatDeviceLine(ua: string): string {
  const { device, browser, os } = parseUserAgent(ua)
  return [device, browser, os].filter(Boolean).join(' · ')
}
