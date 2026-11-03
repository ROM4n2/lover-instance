// The dashboard's entire "routing" budget. A view is ?view=, a conversation is
// ?talker=. Everything else derives from those, so refresh, bookmarks and
// back/forward all work without a router.
//
// Note (design decision D4): the bookmark can carry a conversation fingerprint,
// which is a hash of the account key rather than the key itself, but it is
// still a stable handle to one person. The UI says so next to the field.
const VIEWS = ['overview', 'highlights', 'sentiment', 'jobs']

export function normalizeView(value) {
  return VIEWS.includes(value) ? value : 'overview'
}

export function readState() {
  const params = new URLSearchParams(window.location.search)
  return {
    view: normalizeView(params.get('view')),
    talker: params.get('talker') || '',
  }
}

// writeState updates the address bar without reloading. A view switch is a
// navigation worth a history entry (push); an initial normalisation or a
// talker that the server resolved for us is not (replace), so the back button
// never has to be pressed twice to undo one click.
export function writeState(state, { push = false } = {}) {
  const params = new URLSearchParams()
  if (state.talker) params.set('talker', state.talker)
  params.set('view', normalizeView(state.view))
  const url = `${window.location.pathname}?${params.toString()}`
  const method = push ? 'pushState' : 'replaceState'
  window.history[method]({ view: state.view, talker: state.talker }, '', url)
}
