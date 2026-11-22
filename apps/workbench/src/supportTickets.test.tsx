// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { supportWorkspaces, operationScopes } from './operations';
import { SupportTicketPage } from './SupportTicketPage';
import {
  checkedPage,
  checkedTicket,
  checkedReceipt,
  forgetOriginal,
  keepOriginal,
  originalSlot,
  readOriginal,
  supportApi,
  validText,
} from './supportTickets';
import type { Actor, Original, Ticket } from './supportTickets';
const ws = 'ws_' + 'a'.repeat(32),
  id = 'tkt_' + 'a'.repeat(32),
  other = 'ws_' + 'b'.repeat(32),
  now = '2026-11-22T04:00:00Z';
const owner: Actor = { workspace: ws, user: 'owner', staff: false },
  agent: Actor = { workspace: ws, user: 'agent', staff: true };
const first: Ticket = {
  id,
  workspace_id: ws,
  category: 'account',
  subject: '账户问题',
  state: 'OPEN',
  version: 1,
  created_at: now,
  updated_at: now,
  messages: [{ kind: 'created', version: 1, body: '仅详情可读正文', created_at: now }],
};
const second: Ticket = {
  ...first,
  version: 2,
  messages: [...first.messages, { kind: 'followup', version: 2, body: '新追问', created_at: now }],
};
const third: Ticket = {
  ...second,
  version: 3,
  messages: [...second.messages, { kind: 'reply', version: 3, body: '客服答复', created_at: now }],
};
const closed: Ticket = {
  ...third,
  version: 4,
  state: 'CLOSED',
  messages: [...third.messages, { kind: 'closed', version: 4, body: '', created_at: now }],
};
const card = (t: Ticket) => {
  const { messages, ...c } = t;
  void messages;
  return c;
};
const reply: Original = { id, op: 'reply', expected: 2, key: 'original-reply-key-1' };
const create: Original = { op: 'create', expected: 0, key: 'original-create-key-1' };
let view: HTMLDivElement, root: ReturnType<typeof createRoot>;
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
function render(a = owner, grants = [ws]) {
  return act(() =>
    root.render(
      <SupportTicketPage
        key={JSON.stringify(a)}
        user={a.user}
        workspace={a.workspace}
        staff={a.staff}
        grants={grants}
        onExpired={() => {}}
        onVerifyAccess={() => {}}
      />,
    ),
  );
}
function setup(t = first) {
  vi.spyOn(supportApi, 'list').mockResolvedValue({ items: [card(t)], next_cursor: null });
  vi.spyOn(supportApi, 'detail').mockResolvedValue(t);
}
const button = (l: string) => [...view.querySelectorAll('button')].find((b) => b.textContent === l);
async function click(l: string) {
  const b = button(l);
  expect(b, l).toBeDefined();
  expect(b!.disabled, l).toBe(false);
  await act(() => b!.click());
}
async function field(l: string, value: string) {
  const el = view.querySelector<HTMLInputElement | HTMLTextAreaElement>('[aria-label="' + l + '"]')!;
  await act(() => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value')!.set!.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
}
async function open() {
  await act(() => view.querySelector<HTMLButtonElement>('[data-support-item]')!.click());
}
it('requires support and its permission on the same live grant, independently of general operations read', () => {
  const row = { workspace_id: ws, role: 'support', permissions: ['support_ticket'], version: 1 };
  const access = { enabled: true, access: [row] };
  expect(supportWorkspaces(access)).toEqual([ws]);
  expect(operationScopes(access)).toEqual([]);
  expect(
    supportWorkspaces({
      enabled: true,
      access: [
        { ...row, permissions: [] },
        { ...row, role: 'moderator' },
      ],
    }),
  ).toEqual([]);
  expect(supportWorkspaces({ ...access, enabled: false })).toEqual([]);
});
it('checks UTF-8 and Unicode lengths and permitted multiline fields', () => {
  expect(validText('汉'.repeat(2000))).toBe(true);
  expect(validText('😀'.repeat(1501))).toBe(false);
  expect(validText('line\nline')).toBe(true);
  expect(validText('line\nline', true)).toBe(false);
  expect(validText('\u0001')).toBe(false);
  expect(validText('a'.repeat(121), true)).toBe(false);
});
it('refuses private detail fields in the list, foreign workspaces and misordered message versions', () => {
  expect(() => checkedPage({ items: [first], next_cursor: null }, ws)).toThrow();
  expect(() => checkedPage({ items: [card(first)], next_cursor: null }, other)).toThrow();
  expect(() => checkedTicket({ ...second, messages: [...second.messages].reverse() }, ws)).toThrow();
  expect(() => checkedTicket({ ...closed, state: 'OPEN' }, ws)).toThrow();
});
it('refuses a mismatched original reply receipt despite a matching identity', () => {
  expect(checkedReceipt(third, agent, reply)).toEqual(third);
  expect(() => checkedReceipt(second, agent, reply)).toThrow();
  expect(() => checkedReceipt({ ...third, id: 'tkt_' + 'b'.repeat(32) }, agent, reply)).toThrow();
});
it('posts explicit owner or support request and never resends a password on a receipt read', async () => {
  const f = vi.fn().mockImplementation(async () => new Response('{}'));
  vi.stubGlobal('fetch', f);
  await supportApi.submit(owner, create, { category: 'other', subject: '标题', body: '正文' });
  await supportApi.submit(agent, reply, { body: '回复', password: 'temporary-password' });
  await supportApi.receipt(agent, reply);
  await supportApi.detail(agent, id);
  await supportApi.list(owner, 'more+');
  await supportApi.receipt(owner, create);
  expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({ category: 'other', subject: '标题', body: '正文' });
  expect(JSON.parse(f.mock.calls[1][1].body)).toEqual({
    workspace_id: ws,
    expected_version: 2,
    body: '回复',
    password: 'temporary-password',
  });
  expect(f.mock.calls.map(([, i]) => i.method || 'GET')).toEqual([
    'POST',
    'POST',
    'GET',
    'GET',
    'GET',
    'GET',
  ]);
  expect(f.mock.calls[2][1].body).toBeUndefined();
  expect(f.mock.calls[2][1].headers['Idempotency-Key']).toBe(reply.key);
  for (const [path, init] of f.mock.calls) {
    expect(path).not.toMatch(/project_id|temporary-password|original-reply/);
    expect(new Headers(init.headers).has('X-Project-ID')).toBe(false);
  }
});
it('isolates original keys by account, role and workspace and clears only the matching key', () => {
  keepOriginal(agent, reply);
  expect(readOriginal(owner)).toBeNull();
  expect(readOriginal({ ...agent, workspace: other })).toBeNull();
  expect(readOriginal({ ...agent, staff: false })).toBeNull();
  forgetOriginal(agent, { ...reply, key: 'other' });
  expect(readOriginal(agent)).toEqual(reply);
  expect(() => keepOriginal(agent, reply)).toThrow();
  forgetOriginal(agent, reply);
  expect(readOriginal(agent)).toBeNull();
});
it('does not read staff records when no scoped grant is available', async () => {
  setup();
  await render(agent, []);
  expect(supportApi.list).not.toHaveBeenCalled();
  expect(view.textContent).toContain('没有已授权');
});
it('keeps the operator draft through a CAS conflict, clears the password and requires an explicit rebase', async () => {
  setup();
  const send = vi
    .spyOn(supportApi, 'submit')
    .mockRejectedValueOnce(new ApiError('changed', 409, false, 'SUPPORT_TICKET_CHANGED'))
    .mockResolvedValueOnce(third);
  await render(agent);
  expect(view.querySelector('.support-index')?.textContent).not.toContain(first.messages[0].body);
  await open();
  await field('客服答复', '待发的答复');
  expect((view.querySelector('.support-scope select') as HTMLSelectElement).disabled).toBe(true);
  expect(button('核对客服权限')?.disabled).toBe(true);
  await field('客服当前密码', 'one-time-password');
  vi.mocked(supportApi.detail).mockResolvedValue(second);
  await click('确认发送客服答复');
  expect(send).toHaveBeenCalledTimes(1);
  expect((view.querySelector('[aria-label="客服答复"]') as HTMLTextAreaElement).value).toBe('待发的答复');
  expect((view.querySelector('[aria-label="客服当前密码"]') as HTMLInputElement).value).toBe('');
  expect(button('确认发送客服答复')?.disabled).toBe(true);
  await click('基于当前版本继续编辑');
  await field('客服当前密码', 'second-attempt');
  vi.mocked(supportApi.detail).mockResolvedValue(third);
  await click('确认发送客服答复');
  expect(send.mock.calls.map((call) => call[1].expected)).toEqual([1, 2]);
  expect(readOriginal(agent)).toBeNull();
});
it('recovers a lost reply only by GET, displaying the committed version and separately fetched current closure', async () => {
  setup(second);
  const send = vi.spyOn(supportApi, 'submit').mockRejectedValue(new ApiError('network', 0, true));
  const receipt = vi
    .spyOn(supportApi, 'receipt')
    .mockRejectedValueOnce(new ApiError('absent', 404))
    .mockResolvedValueOnce(third);
  await render(agent);
  await open();
  await field('客服答复', '仅本页可见的答复');
  await field('客服当前密码', 'never-store-password');
  await click('确认发送客服答复');
  const key = send.mock.calls[0][1].key;
  expect(sessionStorage.getItem(originalSlot(agent))).not.toMatch(/never-store-password|仅本页可见的答复/);
  await click('只读核对原工单收据');
  expect(readOriginal(agent)?.key).toBe(key);
  vi.mocked(supportApi.detail).mockResolvedValue(closed);
  await click('只读核对原工单收据');
  expect(send).toHaveBeenCalledTimes(1);
  expect(receipt.mock.calls.map(([, i]) => i.key)).toEqual([key, key]);
  expect(view.querySelector('[data-support-state]')?.getAttribute('data-support-state')).toBe('CLOSED');
  expect(view.querySelector('[data-support-receipt]')?.textContent).toContain('第 3 版');
  expect(readOriginal(agent)).toBeNull();
});
it('removes private detail and drafts if staff no longer has current access', async () => {
  setup();
  await render(agent);
  await open();
  await field('客服答复', '私有草稿');
  vi.mocked(supportApi.list).mockRejectedValue(new ApiError('denied', 404));
  await click('刷新工单');
  expect(view.querySelector('[data-support-ticket]')).toBeNull();
  expect(view.textContent).not.toContain(first.messages[0].body);
  expect(view.querySelector('[aria-label="客服答复"]')).toBeNull();
});
it('requires explicit closure confirmation, allows only owners, and retains the conversation for reading', async () => {
  setup(third);
  const write = vi.spyOn(supportApi, 'submit').mockResolvedValue(closed);
  await render();
  await open();
  await click('关闭工单');
  expect(write).not.toHaveBeenCalled();
  vi.mocked(supportApi.detail).mockResolvedValue(closed);
  await click('确认关闭工单');
  expect(write.mock.calls[0][1].op).toBe('close');
  expect(view.querySelector('[data-support-state]')?.getAttribute('data-support-state')).toBe('CLOSED');
  expect(view.querySelector('[aria-label="用户追问"]')).toBeNull();
});

it('drops a previously authorized detail that arrives after a confirmed scope denial', async () => {
  setup();
  await render(agent);
  let deny!: (e: unknown) => void;
  let release!: (t: Ticket) => void;
  vi.mocked(supportApi.list).mockImplementationOnce(
    () =>
      new Promise((_, reject) => {
        deny = reject;
      }),
  );
  vi.mocked(supportApi.detail).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        release = resolve;
      }),
  );
  await act(() => {
    window.dispatchEvent(new Event('focus'));
  });
  await open();
  await act(async () => {
    deny(new ApiError('denied', 404));
    await Promise.resolve();
  });
  expect(view.querySelector('[data-support-item]')).toBeNull();
  await act(async () => {
    release(first);
    await Promise.resolve();
  });
  expect(view.querySelector('[data-support-ticket]')).toBeNull();
  expect(view.textContent).not.toContain(first.messages[0].body);
});

it('drops an old authorized list when a newer detail read proves that access has ended', async () => {
  setup();
  await render(agent);
  let release!: (p: { items: ReturnType<typeof card>[]; next_cursor: null }) => void;
  vi.mocked(supportApi.list).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        release = resolve;
      }),
  );
  vi.mocked(supportApi.detail).mockRejectedValueOnce(
    new ApiError('disabled', 409, false, 'OPERATIONS_DISABLED'),
  );
  await act(() => {
    window.dispatchEvent(new Event('focus'));
  });
  await open();
  expect(view.querySelector('[data-support-item]')).toBeNull();
  await act(async () => {
    release({ items: [card(first)], next_cursor: null });
    await Promise.resolve();
  });
  expect(view.querySelector('[data-support-item]')).toBeNull();
  expect(view.textContent).not.toContain(first.subject);
});

it('keeps just the original key when a committed reply arrives after authorization was denied', async () => {
  setup(second);
  await render(agent);
  await open();
  await field('客服答复', '待发送的说明');
  await field('客服当前密码', 'temporary-password');
  let deny!: (e: unknown) => void;
  let commit!: (r: Ticket) => void;
  vi.mocked(supportApi.list).mockImplementationOnce(
    () =>
      new Promise((_, reject) => {
        deny = reject;
      }),
  );
  vi.spyOn(supportApi, 'submit').mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        commit = resolve;
      }),
  );
  await act(() => {
    window.dispatchEvent(new Event('focus'));
  });
  await click('确认发送客服答复');
  const original = readOriginal(agent);
  expect(original?.op).toBe('reply');
  await act(async () => {
    deny(new ApiError('denied', 404));
    await Promise.resolve();
  });
  await act(async () => {
    commit(third);
    await Promise.resolve();
  });
  expect(view.querySelector('[data-support-ticket]')).toBeNull();
  expect(view.querySelector('[data-support-receipt]')).toBeNull();
  expect(readOriginal(agent)).toEqual(original);
  expect(sessionStorage.getItem(originalSlot(agent))).not.toMatch(/待发送的说明|temporary-password/);
});
