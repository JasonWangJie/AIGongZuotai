// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { AccountDraftHold } from './accountDraftHold';
import { AccountAccessPage, AccountGovernancePage } from './AccountGovernancePage';
import { governanceApi } from './accountGovernance';
import type { AccountAccess, AccountAppeal, AccountPlan } from './accountGovernance';
import { ApiError, api } from './api';
import { recoveryKey } from './contentAppeals';
import { useStudio } from './useStudio';
import type { AuthSession } from './types';

const user = { id: 'user', email: 'user@example.invalid', name: '本人' },
  session = { auth_enabled: true, account_restricted: true, user } as AuthSession;
const suspended: AccountAccess = {
  target_user_id: 'user',
  state: 'SUSPENDED',
  version: 2,
  restriction: {
    id: 'round-1',
    reason_code: 'manual_review',
    public_notice: '请核对本轮账户限制',
    created_at: 'now',
  },
  can_appeal: true,
};
const open: AccountAccess = { target_user_id: 'user', state: 'OPEN', version: 1, can_appeal: false };
const appeal: AccountAppeal = {
  id: 'appeal',
  target_user_id: 'user',
  restriction_id: 'round-1',
  state: 'OPEN',
  version: 1,
  reason_code: 'request_review',
  statement: '原本人的陈述',
  created_at: 'now',
  updated_at: 'now',
};
const noop = () => {};
let root: ReturnType<typeof createRoot>, view: HTMLDivElement;
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  sessionStorage.clear();
  view = document.createElement('div');
  document.body.append(view);
  root = createRoot(view);
});
afterEach(async () => {
  await act(() => root.unmount());
  view.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const render = (node: React.ReactNode) => act(() => root.render(node));
const own = () =>
  render(<AccountAccessPage session={session} onExpired={noop} refresh={async () => {}} logout={noop} />);
const operator = () =>
  render(
    <AccountGovernancePage
      owner="officer"
      targets={[{ target_user_id: 'user', version: 1 }]}
      onExpired={noop}
      refreshTargets={noop}
    />,
  );
async function click(text: string) {
  const e = [...view.querySelectorAll('button')].find((e) => e.textContent === text);
  expect(e, text).toBeTruthy();
  await act(() => e!.click());
}
async function change(sel: string, value: string) {
  const e = view.querySelector(sel) as HTMLInputElement | HTMLTextAreaElement;
  expect(e, sel).toBeTruthy();
  const proto = e instanceof HTMLInputElement ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
  await act(() => {
    Object.getOwnPropertyDescriptor(proto, 'value')!.set!.call(e, value);
    e.dispatchEvent(new Event('input', { bubbles: true }));
  });
}
it('remembers the interrupted composer only when both authenticated identity and project match', () => {
  const cache = new AccountDraftHold(),
    draft = { prompt: 'private', operation: 'edit' as const },
    empty = { prompt: '', operation: 'generate' as const };
  cache.transition('u', 'p', false, empty);
  expect(cache.transition('', undefined, false, draft)).toBeNull();
  expect(cache.transition('u', undefined, true, empty)).toBeNull();
  expect(cache.transition('u', 'p', false, empty)).toEqual(draft);
  expect(cache.transition('u', 'p', false, empty)).toBeNull();
});
it.each([
  ['another', 'p'],
  ['u', 'other-project'],
])('discards the held composer after different identity or project', (nextOwner, nextProject) => {
  const cache = new AccountDraftHold(),
    draft = { prompt: 'private', operation: 'edit' as const };
  cache.transition('u', 'p', false, draft);
  cache.transition('', undefined, false, draft);
  expect(cache.transition(nextOwner, nextProject, false, draft)).toBeNull();
  expect(cache.transition('u', 'p', false, draft)).toBeNull();
});
it('explicit logout clears the held composer', () => {
  const c = new AccountDraftHold(),
    d = { prompt: 'draft', operation: 'generate' as const };
  c.transition('u', 'p', false, d);
  c.clear();
  c.transition('', undefined, false, d);
  expect(c.transition('u', 'p', false, d)).toBeNull();
});
it('loads only authentication for a restricted session without bootstrapping creative APIs', async () => {
  vi.spyOn(api, 'session').mockResolvedValue(session);
  const bootstrap = vi.spyOn(api, 'bootstrap'),
    runs = vi.spyOn(api, 'runs'),
    tasks = vi.spyOn(api, 'tasks');
  function Probe() {
    const studio = useStudio();
    return (
      <p>
        {String(studio.session?.account_restricted)}:{studio.authState}
      </p>
    );
  }
  await render(<Probe />);
  expect(view.textContent).toBe('true:ready');
  expect(bootstrap).not.toHaveBeenCalled();
  expect(runs).not.toHaveBeenCalled();
  expect(tasks).not.toHaveBeenCalled();
});
it('queries only the original key after an uncertain user appeal; a different round cannot clear it', async () => {
  vi.spyOn(governanceApi, 'access').mockResolvedValue(suspended);
  vi.spyOn(governanceApi, 'history').mockResolvedValue({ items: [] });
  vi.spyOn(governanceApi, 'detail').mockResolvedValue(appeal);
  const write = vi.spyOn(governanceApi, 'apply').mockRejectedValue(new ApiError('响应中断', 0, true));
  const receipt = vi
    .spyOn(governanceApi, 'receipt')
    .mockResolvedValueOnce({ ...appeal, restriction_id: 'new-round' })
    .mockResolvedValue(appeal);
  await own();
  await change('textarea', appeal.statement!);
  await click('提交账户申诉');
  const scope = recoveryKey('account-owner', 'user', 'user'),
    saved = JSON.parse(sessionStorage.getItem(scope)!);
  expect(saved.id).toBe('round-1');
  expect(JSON.stringify(saved)).not.toContain(appeal.statement);
  await click('只读核对原回执');
  expect(sessionStorage.getItem(scope)).toBeTruthy();
  await click('只读核对原回执');
  expect(receipt.mock.calls.map((c) => c[0])).toEqual([saved.key, saved.key]);
  expect(write).toHaveBeenCalledTimes(1);
  expect(sessionStorage.getItem(scope)).toBeNull();
});
it('rejects unscoped state and record details without rendering private information', async () => {
  vi.spyOn(governanceApi, 'access').mockResolvedValue({ ...suspended, target_user_id: 'foreign' });
  vi.spyOn(governanceApi, 'history').mockResolvedValue({ items: [appeal] });
  await own();
  expect(view.textContent).not.toContain(suspended.restriction!.public_notice);
  expect(view.textContent).not.toContain(appeal.statement);
  expect(view.textContent).toContain('不匹配');
});
it('uses only explicit target grants and never reveals a searchable list of other accounts', async () => {
  const access = vi.spyOn(governanceApi, 'access');
  await render(<AccountGovernancePage owner="officer" targets={[]} onExpired={noop} refreshTargets={noop} />);
  expect(access).not.toHaveBeenCalled();
  expect(view.querySelector('input')).toBeNull();
  expect(view.textContent).toContain('没有获授权的目标账户');
});
it('rejects a preview for a foreign target without exposing an execution form', async () => {
  vi.spyOn(governanceApi, 'access').mockResolvedValue(open);
  vi.spyOn(governanceApi, 'history').mockResolvedValue({ items: [] });
  const plan: AccountPlan = {
    id: 'plan',
    target_user_id: 'foreign',
    action: 'suspend',
    expected_version: 1,
    plan_digest: 'digest',
    expires_at: new Date(Date.now() + 60000).toISOString(),
    effect: {
      from_state: 'OPEN',
      to_state: 'SUSPENDED',
      restriction_id: 'round',
      version: 2,
      affected_appeals: 0,
    },
  };
  vi.spyOn(governanceApi, 'plan').mockResolvedValue(plan);
  await operator();
  await change('input[aria-label="账户处置证据编号"]', 'ticket:1');
  await change('textarea', '公开说明');
  await click('预演账户处置');
  expect(view.querySelector('input[type=password]')).toBeNull();
  expect(view.textContent).toContain('不匹配');
});
it('retains the bound target, round and plan after uncertain execution without storing password or statement', async () => {
  vi.spyOn(governanceApi, 'access').mockResolvedValue(open);
  vi.spyOn(governanceApi, 'history').mockResolvedValue({ items: [] });
  const plan: AccountPlan = {
    id: 'plan',
    target_user_id: 'user',
    action: 'suspend',
    expected_version: 1,
    plan_digest: 'unpersisted-digest',
    expires_at: new Date(Date.now() + 60000).toISOString(),
    effect: {
      from_state: 'OPEN',
      to_state: 'SUSPENDED',
      restriction_id: 'round',
      version: 2,
      affected_appeals: 0,
    },
  };
  vi.spyOn(governanceApi, 'plan').mockResolvedValue(plan);
  const write = vi.spyOn(governanceApi, 'execute').mockRejectedValue(new ApiError('响应中断', 0, true));
  const receipt = vi
    .spyOn(governanceApi, 'planReceipt')
    .mockResolvedValue({
      id: 'receipt',
      target_user_id: 'user',
      action: 'suspend',
      effect: plan.effect,
      plan_id: 'plan',
      executed_at: 'time',
    });
  await operator();
  await change('input[aria-label="账户处置证据编号"]', 'ticket:1');
  await change('textarea', '公开说明');
  await click('预演账户处置');
  await act(() => view.querySelector<HTMLInputElement>('input[type=checkbox]')!.click());
  await change('input[type=password]', 'private-password');
  await click('确认执行账户处置');
  const key = recoveryKey('account-operator', 'officer', 'user');
  const value = sessionStorage.getItem(key)!;
  expect(value).toContain('round');
  expect(value).not.toMatch(/公开说明|private-password|unpersisted-digest/);
  expect(view.querySelector<HTMLInputElement>('input[type=password]')?.value).toBe('');
  await click('只读核对原回执');
  expect(receipt).toHaveBeenCalledWith('user', 'plan');
  expect(write).toHaveBeenCalledTimes(1);
  expect(sessionStorage.getItem(key)).toBeNull();
});
it('sends the original appeal key only as a header and no creative workspace identity', async () => {
  const f = vi.fn((_url: string, _init: RequestInit) => Promise.resolve(Response.json({})));
  vi.stubGlobal('fetch', f);
  await governanceApi.receipt('origin-key');
  await governanceApi.planReceipt('usr-id', 'plan-id');
  expect(f.mock.calls.map((c) => c[0])).toEqual([
    '/api/v1/account/appeals/receipt',
    '/api/v1/operations/account-targets/usr-id/plans/plan-id/receipt',
  ]);
  expect(new Headers(f.mock.calls[0][1].headers).get('Idempotency-Key')).toBe('origin-key');
  expect(f.mock.calls.every((c) => !new Headers(c[1].headers).has('X-Project-ID'))).toBe(true);
});

it('preserves the previous bound draft when the project clears before the expired identity', () => {
  const hold = new AccountDraftHold();
  const draft = { prompt: 'original bound text', operation: 'generate' as const };
  const empty = { prompt: '', operation: 'generate' as const };
  hold.transition('owner', 'project', false, empty);
  hold.transition('owner', undefined, false, draft);
  hold.transition('', undefined, false, empty);
  hold.transition('owner', undefined, true, empty);
  expect(hold.transition('owner', 'project', false, empty)).toEqual(draft);
});
