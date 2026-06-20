export function joinURL(base: string, path: string): string {
  const url = new URL(base)
  const cleanPath = `/${path}`.replace(/\/+/g, '/')
  url.pathname = `${url.pathname.replace(/\/+$/, '')}${cleanPath}`
  url.search = ''
  url.hash = ''
  return url.toString()
}

export function apiURL(path: string): string {
  const normalized = path.startsWith('/') ? path : `/${path}`
  const current = window.location.pathname
  const reportIndex = current.indexOf('/report/')
  const embedIndex = current.indexOf('/embed')
  let prefix = ''
  if (reportIndex >= 0) prefix = current.slice(0, reportIndex)
  if (embedIndex >= 0) prefix = current.slice(0, embedIndex)
  return `${prefix}/api${normalized}`.replace(/\/+/g, '/')
}

export function withNonce(url: string): string {
  const next = new URL(url)
  next.searchParams.set('nonce', crypto.randomUUID())
  return next.toString()
}
