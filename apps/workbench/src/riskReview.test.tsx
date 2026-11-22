// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { ApiError, request } from './api';
import { recoveryKey } from './contentAppeals';
import { operationScopes } from './operations';
import { requestAdvice } from './requestFeedback';
import { RiskReviewPage, RiskReviewOperations } from './RiskReviewPage';
import { captureRiskInput, rememberRisk, riskOriginal, riskSignal, setRiskScope } from './riskOriginal';
import { riskApi } from './riskReview';
import type { RiskAppeal, RiskEvidence, RiskPlan } from './riskReview';

const scope = { owner: 'person', workspace: 'workspace', project: 'project' };
const noop = () => {};
const source = { ...scope, quote: 'quote', onExpired: noop };
const privatePrompt = 'private generation input';
const input = {
  method: 'POST',
  body: JSON.stringify({ project_id: scope.project, quote_id: 'quote', input: privatePrompt }),
  headers: { 'Idempotency-Key': 'original-key' },
};
function proof(): RiskEvidence {
  return {
    quote_id: 'quote',
    project_id: scope.project,
    workspace_id: scope.workspace,
    reason_codes: ['HELD_LIMIT', 'RATE_LIMIT'],
    policy_version: 1,
    amount_micros: '200000',
    expires_at: new Date(Date.now() + 580000).toISOString(),
    allowed_to_appeal: true,
  };
}
function appeal(fields: Partial<RiskAppeal> = {}): RiskAppeal {
  return { ...proof(), id: 'appeal', reason_code: 'limit_error', state: 'OPEN', version: 1, ...fields };
}
function approved(fields: Partial<RiskAppeal> = {}) {
  return appeal({ state: 'APPROVED', version: 2, exemption_state: 'AVAILABLE', ...fields });
}
let root: ReturnType<typeof createRoot>, view: HTMLDivElement;
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  sessionStorage.clear();
  setRiskScope(scope);
  view = document.createElement('div');
  document.body.append(view);
  root = createRoot(view);
});
afterEach(async () => {
  await act(() => root.unmount());
  view.remove();
  setRiskScope(null);
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const render = (node: React.ReactNode) => act(() => root.render(node));
function btn(label: string) {
  return [...view.querySelectorAll('button')].find((b) => b.textContent === label);
}
async function click(label: string) {
  const b = btn(label);
  expect(b, label).toBeTruthy();
  expect(b!.disabled).toBe(false);
  await act(() => b!.click());
}
async function pick() {
  await act(() => view.querySelector<HTMLButtonElement>('.appeal-rows button')!.click());
}
async function value(selector: string, val: string) {
  const el = view.querySelector(selector) as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
  expect(el).toBeTruthy();
  const proto = Object.getPrototypeOf(el);
  await act(() => {
    Object.getOwnPropertyDescriptor(proto, 'value')!.set!.call(el, val);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
  });
}
function ready(a = appeal()) {
  vi.spyOn(riskApi, 'list').mockResolvedValue({ items: [a] });
  vi.spyOn(riskApi, 'detail').mockResolvedValue(a);
}
function denied(path = '/tasks', init: RequestInit = input) {
  rememberRisk(captureRiskInput(path, init));
}
async function readCurrent() {
  await render(<RiskReviewPage {...source} quote="" />);
  await pick();
}
it.each(['CREDIT_RISK_HELD_LIMIT', 'CREDIT_RISK_RATE_LIMIT'])(
  'captures only a real scoped HTTP 429 %s response and keeps payload in page memory',
  async (code) => {
    const notify = vi.fn();
    window.addEventListener(riskSignal, notify);
    try {
      const f = vi.fn().mockResolvedValue(Response.json({ error: { code } }, { status: 429 }));
      vi.stubGlobal('fetch', f);
      expect(riskOriginal(scope, 'quote')).toBeUndefined();
      await expect(request('/tasks', input)).rejects.toMatchObject({ status: 429, uncertain: false, code });
      expect(notify).toHaveBeenCalledTimes(1);
      expect(riskOriginal(scope, 'quote')).toMatchObject({ key: 'original-key', body: input.body });
      expect(sessionStorage.length).toBe(0);
      expect(requestAdvice({ status: 429, code }, false)).toContain('原报价');
      await expect(
        request('/tasks', { ...input, headers: { 'Idempotency-Key': 'new-key' } }),
      ).rejects.toMatchObject({ code: 'CREDIT_RISK_ORIGINAL_REQUIRED' });
      await expect(
        request('/tasks', { ...input, body: input.body.replace(privatePrompt, 'changed') }),
      ).rejects.toMatchObject({ code: 'CREDIT_RISK_ORIGINAL_REQUIRED' });
      expect(f).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener(riskSignal, notify);
    }
  },
);
it.each([
  [429, 'ADMISSION_LIMIT'],
  [503, 'CREDIT_RISK_HELD_LIMIT'],
])('does not open a risk case on unrelated failures %s %s', async (status, code) => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(Response.json({ error: { code } }, { status: Number(status) })),
  );
  await expect(request('/tasks', input)).rejects.toBeInstanceOf(ApiError);
  expect(riskOriginal(scope, 'quote')).toBeUndefined();
});
it('discards late refusals from another account and does not accept a mismatched project', () => {
  const early = captureRiskInput('/tasks', input);
  denied('/auth/password/change');
  expect(riskOriginal(scope, 'quote')).toBeUndefined();
  denied('/tasks', { ...input, headers: { ...input.headers, 'X-Project-ID': 'other' } });
  expect(riskOriginal(scope, 'quote')).toBeUndefined();
  setRiskScope({ ...scope, owner: 'different' });
  rememberRisk(early);
  expect(riskOriginal(scope, 'quote')).toBeUndefined();
});
it.each([
  '/tasks',
  '/runs',
  '/conversations/conversation/runs',
  '/projects/project/workflow-runs/flow/nodes/video/execute',
])('replays exact frozen body, header and original key only on confirmation for %s', async (path) => {
  const headers: Record<string, string> = { ...input.headers };
  if (path.includes('/conversations/') || path.includes('/projects/'))
    headers['X-Project-ID'] = scope.project;
  const original = { ...input, headers };
  denied(path, original);
  ready(approved());
  const post = vi.fn((_url: string, _init: RequestInit) =>
    Promise.resolve(Response.json({ id: 'admitted', project_id: scope.project })),
  );
  vi.stubGlobal('fetch', post);
  await readCurrent();
  expect(post).not.toHaveBeenCalled();
  expect(view.textContent).not.toContain(privatePrompt);
  await click('按原报价确认提交原请求');
  expect(post).toHaveBeenCalledTimes(1);
  const [url, sent] = post.mock.calls[0];
  expect(url).toBe('/api/v1' + path);
  expect(sent.body).toBe(original.body);
  expect(new Headers(sent.headers).get('Idempotency-Key')).toBe('original-key');
  expect(new Headers(sent.headers).get('X-Project-ID')).toBe(headers['X-Project-ID'] || null);
  expect(view.textContent).toContain('原请求已受理');
  expect(btn('按原报价确认提交原请求')).toBeUndefined();
});
it.each([
  ['EXPIRED', '已到期'],
  ['CONSUMED', '许可已消费'],
  ['INACTIVE', '许可已失效'],
] as const)('blocks an explicit replay for server exemption %s', async (state, label) => {
  denied();
  ready(approved({ exemption_state: state }));
  await readCurrent();
  expect(view.textContent).toContain(label);
  expect(btn('按原报价确认提交原请求')).toBeUndefined();
});
it('does not send an available permit after its observed deadline or on a new page without the original', async () => {
  denied();
  const detail = vi.spyOn(riskApi, 'detail');
  vi.spyOn(riskApi, 'list').mockResolvedValue({ items: [approved()] });
  detail.mockResolvedValue(approved({ expires_at: new Date(Date.now() - 10).toISOString() }));
  await readCurrent();
  expect(view.textContent).toContain('原报价已到期');
  await render(null);
  setRiskScope(null);
  setRiskScope(scope);
  detail.mockResolvedValue(approved());
  await readCurrent();
  expect(view.textContent).toContain('没有原提交正文');
  expect(btn('按原报价确认提交原请求')).toBeUndefined();
});
it('keeps lost generation acknowledgement read-only and exposes a consumed permit after refresh', async () => {
  denied();
  const a = approved();
  ready(a);
  const send = vi.fn().mockRejectedValue(TypeError('local network loss'));
  vi.stubGlobal('fetch', send);
  await readCurrent();
  await click('按原报价确认提交原请求');
  expect(riskOriginal(scope, 'quote')?.phase).toBe('unknown');
  expect(view.textContent).toContain('回执待核对');
  await expect(request('/tasks', input)).rejects.toMatchObject({ code: 'CREDIT_RISK_ORIGINAL_REQUIRED' });
  vi.mocked(riskApi.detail).mockResolvedValue({ ...a, exemption_state: 'CONSUMED' });
  await click('刷新复核');
  expect(view.textContent).toContain('许可已消费');
  expect(send).toHaveBeenCalledTimes(1);
});
it('reconciles an unknown owner application by the original key, with no private inputs persisted', async () => {
  const a = appeal();
  ready(a);
  vi.spyOn(riskApi, 'eligibility').mockResolvedValue(proof());
  const write = vi.spyOn(riskApi, 'apply').mockRejectedValue(new ApiError('未确认', 0, true)),
    ack = vi.spyOn(riskApi, 'receipt').mockResolvedValue(a);
  await render(<RiskReviewPage {...source} />);
  await value('textarea', 'private statement');
  await click('提交额度风控复核');
  const saved = sessionStorage.getItem(recoveryKey('risk-user', scope.owner, scope.workspace, scope.project));
  expect(saved).not.toContain('private statement');
  expect(saved).toContain(write.mock.calls[0][4]);
  await render(null);
  await render(<RiskReviewPage {...source} quote="" />);
  await click('只读核对原回执');
  expect(ack).toHaveBeenCalledWith(scope.project, write.mock.calls[0][4]);
  expect(write).toHaveBeenCalledTimes(1);
});
it('rejects foreign proof and detail, withholding private statements and mutation buttons', async () => {
  ready(appeal());
  vi.mocked(riskApi.detail).mockResolvedValue(appeal({ workspace_id: 'another', statement: privatePrompt }));
  vi.spyOn(riskApi, 'eligibility').mockResolvedValue({ ...proof(), project_id: 'another' });
  await render(<RiskReviewPage {...source} />);
  await pick();
  expect(view.textContent).not.toContain(privatePrompt);
  expect(btn('提交额度风控复核')).toBeUndefined();
});
it('does not offer a new review without any server-identified risk rule', async () => {
  ready();
  vi.spyOn(riskApi, 'eligibility').mockResolvedValue({ ...proof(), reason_codes: [] });
  await render(<RiskReviewPage {...source} />);
  expect(btn('提交额度风控复核')).toBeUndefined();
});
it('retains the scoped reviewer role without granting generic operations read or finance', () => {
  const grant = {
    workspace_id: scope.workspace,
    version: 1,
    role: 'risk_reviewer',
    permissions: ['risk_review'],
  };
  expect(operationScopes({ enabled: true, access: [grant] })).toEqual([
    { id: scope.workspace, roles: ['risk_reviewer'], permissions: ['risk_review'] },
  ]);
  expect(operationScopes({ enabled: true, access: [{ ...grant, role: 'moderator' }] })).toEqual([]);
});
async function preview(plan: RiskPlan) {
  ready();
  vi.spyOn(riskApi, 'preview').mockResolvedValue(plan);
  await render(
    <RiskReviewOperations owner="reviewer" workspace={scope.workspace} onExpired={noop} onBusy={noop} />,
  );
  await pick();
  await value('select', plan.action);
  await value('.appeal-compose input', 'ticket:1');
  await value('.appeal-compose textarea', '公开决定');
  await click('预演额度风控处置');
}
const plan = (action: 'approve' | 'deny'): RiskPlan => ({
  id: 'plan',
  workspace_id: scope.workspace,
  appeal_id: 'appeal',
  expected_version: 1,
  action,
  outcome: action === 'approve' ? 'APPROVED' : 'DENIED',
  expires_at: proof().expires_at,
  plan_digest: 'digest',
});
it('hides password execution for a preview belonging to another scope', async () => {
  await preview({ ...plan('approve'), workspace_id: 'foreign' });
  expect(view.querySelector('input[type=password]')).toBeNull();
  expect(view.textContent).toContain('不匹配');
});
it.each(['approve', 'deny'] as const)(
  'requires review and uses GET receipt only after an uncertain %s operation',
  async (action) => {
    const p = plan(action);
    const send = vi.spyOn(riskApi, 'execute').mockRejectedValue(new ApiError('未确认', 0, true));
    const read = vi
      .spyOn(riskApi, 'opsReceipt')
      .mockResolvedValue({
        id: 'receipt',
        workspace_id: scope.workspace,
        appeal_id: 'appeal',
        plan_id: 'plan',
        action,
        outcome: p.outcome,
        version: 2,
      });
    await preview(p);
    expect(btn('确认执行风控处置')!.disabled).toBe(true);
    await act(() => view.querySelector<HTMLInputElement>('input[type=checkbox]')!.click());
    await value('input[type=password]', 'private-password');
    await click('确认执行风控处置');
    const saved = sessionStorage.getItem(recoveryKey('risk-ops', 'reviewer', scope.workspace));
    expect(saved).not.toMatch(/private-password|公开决定|digest/);
    expect(view.querySelector<HTMLInputElement>('input[type=password]')?.value).toBe('');
    await click('只读核对原回执');
    expect(send).toHaveBeenCalledTimes(1);
    expect(read).toHaveBeenCalledWith(scope.workspace, 'appeal', 'plan');
    expect(view.textContent).toContain('已核对风控处置收据');
  },
);
