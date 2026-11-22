// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { createHash, webcrypto } from 'node:crypto';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { AccountDataExport } from './AccountDataExportPanel';
import * as transport from './accountDataExport';
const a = 'usr_' + 'a'.repeat(32),
  b = 'usr_' + 'b'.repeat(32),
  time = '2026-11-22T05:00:00.123Z',
  secret = 'fixture-current-password';
const profile = {
  format: 'studio.account-export.v1',
  section: 'profile',
  account_id: a,
  read_at: time,
  created_through: time,
  items: [{ id: a, name: '本人', email: 'mine@example.invalid', created_at: time, email_verified_at: null }],
  next_cursor: null,
};
const events = {
  ...profile,
  section: 'security_events',
  items: [{ id: 'event1', action: 'account.login', created_at: time }],
  next_cursor: 'next-cursor',
};
const appeal = {
  ...profile,
  section: 'account_appeals',
  items: [
    {
      id: 'appeal',
      restriction_id: 'round',
      state: 'OPEN',
      outcome: '',
      version: 1,
      reason_code: 'request_review',
      statement: '本人说明',
      public_reply: '',
      created_at: time,
      updated_at: time,
    },
  ],
};
const req = {
  owner: a,
  section: 'profile' as const,
  range: { page: 1 },
  password: secret,
  signal: new AbortController().signal,
};
const enc = (x: unknown) => new TextEncoder().encode(JSON.stringify(x));
function response(x: unknown, section = 'profile', extra: Record<string, string> = {}) {
  const data = enc(x),
    digest = createHash('sha256').update(data).digest('hex');
  return new Response(data, {
    headers: {
      'Content-Type': 'application/json; charset=utf-8',
      'Content-Disposition': 'attachment; filename="account-' + section + '.json"',
      'Cache-Control': 'private, no-store',
      'X-Content-SHA256': digest,
      ...extra,
    },
  });
}
let view: HTMLDivElement, root: ReturnType<typeof createRoot>;
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('crypto', webcrypto);
  sessionStorage.clear();
  localStorage.clear();
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
function page(user = a) {
  return act(() =>
    root.render(<AccountDataExport key={user} owner={user} restricted={user === b} onExpired={() => {}} />),
  );
}
const pwd = () => view.querySelector<HTMLInputElement>('#account-export-password')!;
async function select(value: string) {
  await act(() => {
    const select = view.querySelector('select')!;
    select.value = value;
    select.dispatchEvent(new Event('change', { bubbles: true }));
  });
}
async function submit() {
  await act(() => {
    pwd().value = secret;
    view.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  });
}
it('sends a single reauthenticated account-only POST and validates the exact attachment bytes', async () => {
  const fetcher = vi.fn().mockResolvedValue(response(profile));
  vi.stubGlobal('fetch', fetcher);
  const r = await transport.requestDataPage(req);
  expect(r.count).toBe(1);
  expect(r.digest).toBe(createHash('sha256').update(enc(profile)).digest('hex'));
  expect(r.blob.size).toBe(enc(profile).length);
  expect(fetcher).toHaveBeenCalledTimes(1);
  const [url, options] = fetcher.mock.calls[0];
  expect(url).toBe('/api/v1/auth/data-export');
  expect(options.method).toBe('POST');
  expect(options.cache).toBe('no-store');
  expect(options.redirect).toBe('error');
  expect(JSON.parse(options.body)).toEqual({ section: 'profile', password: secret });
  expect(Object.keys(options.headers)).toEqual(['Content-Type', 'Accept']);
});
it.each([
  ['wrong digest', { 'X-Content-SHA256': 'a'.repeat(64) }],
  ['public cache', { 'Cache-Control': 'public' }],
  ['wrong attachment', { 'Content-Disposition': 'attachment; filename="different.json"' }],
  ['excess bytes', { 'Content-Length': '524289' }],
] as const)('rejects %s without downloading', async (_label, change) => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(profile, 'profile', change)));
  await expect(transport.requestDataPage(req)).rejects.toThrow();
});
it('bounds an actual oversized streaming body even when no declared length is given', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ ...profile, extra: 'x'.repeat(524288) })));
  await expect(transport.requestDataPage(req)).rejects.toThrow();
});
it.each([
  ['foreign account', { ...profile, account_id: b }],
  ['foreign profile', { ...profile, items: [{ ...profile.items[0], id: b }] }],
  ['foreign section', { ...profile, section: 'account_appeals' }],
  ['extra field', { ...profile, token: 'secret' }],
  ['private row column', { ...profile, items: [{ ...profile.items[0], ip: 'private' }] }],
  ['profile cursor', { ...profile, next_cursor: 'foreign' }],
])('rejects %s', (_label, value) => {
  expect(() => transport.checkDataPage(value, a, 'profile', { page: 1 })).toThrow();
});
it('permits whitelisted appeal statements and event pages while pinning the original cutoff', () => {
  expect(transport.checkDataPage(appeal, a, 'account_appeals', { page: 1 }).count).toBe(1);
  expect(transport.checkDataPage(events, a, 'security_events', { page: 1 }).next).toBe(events.next_cursor);
  expect(() =>
    transport.checkDataPage(events, a, 'security_events', {
      page: 2,
      cursor: 'previous',
      cutoff: '2020-01-01T00:00:00Z',
    }),
  ).toThrow();
  expect(() =>
    transport.checkDataPage(events, a, 'security_events', {
      page: 2,
      cursor: events.next_cursor,
      cutoff: time,
    }),
  ).toThrow();
  expect(() =>
    transport.checkDataPage({ ...events, items: Array(101).fill(events.items[0]) }, a, 'security_events', {
      page: 1,
    }),
  ).toThrow();
});
it('does not download an error response, echo private server text, retry, or retain the password', async () => {
  const f = vi
    .fn()
    .mockResolvedValue(
      new Response(JSON.stringify({ error: { code: 'ACCOUNT_EXPORT_PASSWORD', message: secret } }), {
        status: 403,
      }),
    );
  vi.stubGlobal('fetch', f);
  const download = vi.spyOn(transport, 'downloadDataPage').mockImplementation(() => {});
  await page();
  expect(f).not.toHaveBeenCalled();
  await submit();
  expect(f).toHaveBeenCalledTimes(1);
  expect(pwd().value).toBe('');
  expect(view.textContent).toContain('当前密码不正确');
  expect(view.textContent).not.toContain(secret);
  expect(download).not.toHaveBeenCalled();
  expect(sessionStorage.length + localStorage.length).toBe(0);
});
it('requires new confirmation and password per page, and drops an old cursor upon identity change', async () => {
  const f = vi
    .fn()
    .mockResolvedValueOnce(response(events, 'security_events'))
    .mockResolvedValueOnce(response({ ...events, items: [], next_cursor: null }, 'security_events'));
  vi.stubGlobal('fetch', f);
  vi.spyOn(transport, 'downloadDataPage').mockImplementation(() => {});
  await page();
  await select('security_events');
  await submit();
  expect(pwd().value).toBe('');
  await vi.waitFor(async () => {
    await act(() => new Promise<void>(resolve => setTimeout(resolve, 0)));
    expect(view.querySelector('[data-export-count]'), view.querySelector('[role="alert"]')?.textContent || 'digest pending').not.toBeNull();
  });
  const next = [...view.querySelectorAll('button')].find((x) => x.textContent === '准备下一页')!;
  await act(() => next.click());
  expect(f).toHaveBeenCalledTimes(1);
  expect(view.textContent).toContain('第 2 页');
  await submit();
  expect(JSON.parse(f.mock.calls[1][1].body).cursor).toBe(events.next_cursor);
  await page(b);
  expect(view.textContent).toContain('受限会话');
  expect(view.querySelector('select')!.value).toBe('profile');
  expect(view.textContent).toContain('第 1 页');
  expect(pwd().value).toBe('');
  expect(f).toHaveBeenCalledTimes(2);
});
it('ignores a late export after navigating away or switching identity', async () => {
  let done!: (r: Response) => void;
  const f = vi.fn().mockReturnValue(new Promise((resolve) => (done = resolve)));
  vi.stubGlobal('fetch', f);
  const download = vi.spyOn(transport, 'downloadDataPage').mockImplementation(() => {});
  await page();
  await submit();
  expect(pwd().value).toBe('');
  await page(b);
  expect(f.mock.calls[0][1].signal.aborted).toBe(true);
  await act(() => done(response(profile)));
  expect(download).not.toHaveBeenCalled();
});
