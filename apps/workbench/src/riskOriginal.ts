type Scope = { owner: string; workspace: string; project: string };
type Original = Scope & {
  quote: string;
  path: string;
  body: string;
  key: string;
  projectHeader?: string;
  phase: 'blocked' | 'sending' | 'unknown' | 'accepted';
  result?: string;
};
const slots = new Map<string, Original>();
let active: Scope | null = null;
const slot = (s: Scope, q: string) => JSON.stringify([s.owner, s.workspace, s.project, q]);
export const riskCodes = ['CREDIT_RISK_HELD_LIMIT', 'CREDIT_RISK_RATE_LIMIT'];
export const riskSignal = 'studio-risk-rejection';
export function setRiskScope(s: Scope | null) {
  if (active?.owner !== s?.owner || active?.workspace !== s?.workspace) slots.clear();
  active = s;
}
export function captureRiskInput(path: string, init?: RequestInit) {
  if (
    !active ||
    !active.project ||
    init?.method !== 'POST' ||
    typeof init.body !== 'string' ||
    !/^\/(runs|tasks)$|^\/conversations\/[^/]+\/runs$|^\/projects\/[^/]+\/workflow-runs\/[^/]+\/nodes\/[^/]+\/execute$/.test(
      path,
    )
  )
    return;
  const headers = new Headers(init.headers),
    key = headers.get('Idempotency-Key'),
    projectHeader = headers.get('X-Project-ID') || undefined;
  if (!key) return;
  try {
    const body = JSON.parse(init.body),
      project = body.project_id || projectHeader;
    if (
      project !== active.project ||
      (projectHeader && projectHeader !== active.project) ||
      (path.startsWith('/projects/') && path.split('/')[2] !== encodeURIComponent(active.project)) ||
      typeof body.quote_id !== 'string' ||
      !body.quote_id
    )
      return;
    return {
      ...active,
      quote: body.quote_id,
      path,
      body: init.body,
      key,
      projectHeader,
      phase: 'blocked' as const,
    };
  } catch {
    return;
  }
}
export function riskChangedInput(i: ReturnType<typeof captureRiskInput>) {
  const old = i && slots.get(slot(i, i.quote));
  return (
    !!old &&
    !!i &&
    (old.key !== i.key ||
      old.path !== i.path ||
      old.body !== i.body ||
      old.projectHeader !== i.projectHeader ||
      old.phase === 'unknown')
  );
}
export function rememberRisk(i: ReturnType<typeof captureRiskInput>) {
  if (!i || active?.owner !== i.owner || active?.workspace !== i.workspace || active?.project !== i.project)
    return;
  const id = slot(i, i.quote),
    old = slots.get(id);
  if (!old) {
    if (slots.size >= 48) {
      const first = [...slots.entries()].find(([, v]) => v.phase !== 'unknown' && v.phase !== 'sending');
      if (first) slots.delete(first[0]);
      else return;
    }
    slots.set(id, i);
  }
  window.dispatchEvent(new CustomEvent(riskSignal, { detail: i.quote }));
}
export function riskOriginal(s: Scope, q: string) {
  return slots.get(slot(s, q));
}
export function matchRiskScope(s: Scope) {
  return (
    !!active && s.owner === active.owner && s.project === active.project && s.workspace === active.workspace
  );
}
export function claimRisk(s: Scope, q: string) {
  const i = riskOriginal(s, q);
  if (!i || i.phase !== 'blocked' || !matchRiskScope(s)) return null;
  i.phase = 'sending';
  return i;
}
export function riskResult(i: Original, state: Original['phase'], id?: string) {
  if (slots.get(slot(i, i.quote)) === i) {
    i.phase = state;
    i.result = id;
  }
}
