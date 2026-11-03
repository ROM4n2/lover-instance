// Thin wrapper over the dashboard's JSON API.
//
// Every failure is normalised to an Error carrying the server's
// {code, message, hint}, so a view renders the hint the server wrote instead of
// inventing its own copy and drifting from the backend. The API never returns
// message text or a talker, and nothing here adds any.
const BASE = '/api'

async function request(path, options = {}) {
  const response = await fetch(BASE + path, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  })

  // Read the body once as text: a 204/empty success has no JSON, and an error
  // body must be parsed even when the status is not ok.
  const text = await response.text()
  let body = null
  if (text) {
    try {
      body = JSON.parse(text)
    } catch {
      body = null
    }
  }

  if (!response.ok) {
    const error = new Error((body && body.message) || `请求失败（HTTP ${response.status}）`)
    error.status = response.status
    error.code = body && body.code
    error.hint = body && body.hint
    error.body = body
    throw error
  }
  return body
}

export function getHealth() {
  return request('/health')
}

export function getOverview(talker, modelVersion) {
  const params = new URLSearchParams()
  if (talker) params.set('talker', talker)
  if (modelVersion) params.set('model_version', modelVersion)
  const query = params.toString()
  return request(`/overview${query ? `?${query}` : ''}`)
}

export function createJob(payload = {}) {
  return request('/jobs', { method: 'POST', body: JSON.stringify(payload) })
}

export function getJob(id) {
  return request(`/jobs/${id}`)
}

export function cancelJob(id) {
  return request(`/jobs/${id}`, { method: 'DELETE' })
}

export function exportReport(payload = {}) {
  return request('/export', { method: 'POST', body: JSON.stringify(payload) })
}

export function getHighlights(talker) {
  const params = new URLSearchParams()
  if (talker) params.set('talker', talker)
  const query = params.toString()
  return request(`/highlights${query ? `?${query}` : ''}`)
}

export function loadDemo() {
  return request('/demo', { method: 'POST' })
}

export function scanExport(autoImport = false) {
  return request(`/scan-export${autoImport ? '?import=true' : ''}`, { method: 'POST' })
}

export async function uploadExport(file) {
  const form = new FormData()
  form.append('file', file)
  const response = await fetch(BASE + '/import', {
    method: 'POST',
    body: form,
  })
  const text = await response.text()
  let body = null
  if (text) {
    try { body = JSON.parse(text) } catch {}
  }
  if (!response.ok) {
    const error = new Error((body && body.message) || `上传失败（HTTP ${response.status}）`)
    error.status = response.status
    error.code = body && body.code
    error.hint = body && body.hint
    throw error
  }
  return body
}
