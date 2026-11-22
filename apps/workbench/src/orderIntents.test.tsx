// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { OrderIntentsPage } from './OrderIntentsPage';
import {
  beginTicket,
  checkCatalog,
  checkIntentReceipt,
  credit,
  finishTicket,
  fixedUnits,
  intentSlot,
  money,
  orderApi,
  productIdentity,
  readJournal,
} from './orderIntents';
import type { Catalog, IntentTicket, Order, Product } from './orderIntents';
const p: Product = {
  id: 'credit-basic',
  name: '创作点数',
  currency: 'CNY',
  currency_exponent: 2,
  amount_minor: '1200',
  credit_micros: '10000001',
  catalog_version: 'v1',
  catalog_digest: 'a'.repeat(64),
  product_digest: 'b'.repeat(64),
};
const catalog: Catalog = {
  enabled: true,
  can_create: true,
  can_manage: true,
  catalog_version: 'v1',
  catalog_digest: p.catalog_digest,
  items: [p],
};
const o: Order = {
  id: 'ord_' + 'a'.repeat(32),
  product: p,
  state: 'PENDING',
  version: 1,
  created_at: '2026-11-22T10:00:00Z',
};
const cancelled: Order = { ...o, state: 'CANCELLED', version: 2, cancelled_at: '2026-11-22T11:00:00Z' };
const ticket: IntentTicket = { operation: 'create', key: 'original-create-key', product: productIdentity(p) };
const cancel: IntentTicket = { operation: 'cancel', key: 'another-cancel-key', id: o.id, version: 1 };
const props = { user: 'buyer', workspace: 'home', enabled: true, onExpired: vi.fn() };
const slot = intentSlot(props.user, props.workspace);
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
const render = (enabled = true) => act(() => root.render(<OrderIntentsPage {...props} enabled={enabled} />));
const button = (text: string) => [...view.querySelectorAll('button')].find((b) => b.textContent === text);
async function click(text: string) {
  const b = button(text);
  expect(b, text).toBeDefined();
  expect(b!.disabled, text).toBe(false);
  await act(() => b!.click());
}
function unboundProjection(id: string) {
  return {
    order_id: id, currency: 'CNY', currency_exponent: 2, environment: '',
    payment_state: 'UNBOUND', refund_state: 'NONE', credit_state: 'NOT_ISSUED',
    paid_minor: '0', refunded_minor: '0', granted_credit_micros: '0', reversed_credit_micros: '0',
  };
}
function setup(c = catalog, rows: Order[] = []) {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const match = path.match(/^\/api\/v1\/order-intents\/(ord_[0-9a-f]{32})\/payment-status$/);
    if (!match) throw Error('Unexpected fixture request');
    return jsonResponse(unboundProjection(match[1]));
  }));
  vi.spyOn(orderApi, 'catalog').mockResolvedValue(c);
  vi.spyOn(orderApi, 'list').mockResolvedValue({ items: rows });
  vi.spyOn(orderApi, 'detail').mockResolvedValue(o);
}
it.each([
  ['JPY', 0, '28', '28'],
  ['CNY', 2, '1', '0.01'],
  ['KWD', 3, '12001', '12.001'],
  ['HKD', 2, '1000000000000', '10000000000.00'],
] as const)('keeps %s minor units separate from six-place CREDIT', (currency, scale, raw, formatted) => {
  const product = { ...p, currency, currency_exponent: scale, amount_minor: raw };
  checkCatalog({ ...catalog, items: [product] });
  expect(money(product)).toBe(currency + ' ' + formatted);
  expect(credit(product)).toBe('10.000001 CREDIT');
});
it('keeps all decimal digits without parsing with floating point', () => {
  expect(fixedUnits('9007199254740993', 6)).toBe('9007199254.740993');
  expect(() => fixedUnits('1e2', 2)).toThrow();
});
it.each([
  { amount_minor: '1.25' },
  { currency: 'JPY' },
  { credit_micros: '1000000000000001' },
  { product_digest: 'invalid' },
  { catalog_digest: 'c'.repeat(64) },
])('rejects an invalid product snapshot %o', (change) => {
  expect(() => checkCatalog({ ...catalog, items: [{ ...p, ...change }] })).toThrow();
});
it('scopes the retained ticket to this account and workspace and clears only its own request', () => {
  beginTicket(slot, ticket);
  expect(readJournal(intentSlot('other', 'home'))).toEqual({});
  expect(readJournal(intentSlot('buyer', 'another'))).toEqual({});
  expect(() => beginTicket(slot, cancel)).toThrow();
  finishTicket(slot, cancel, true);
  expect(readJournal(slot).pending).toEqual(ticket);
  finishTicket(slot, ticket, true);
  beginTicket(slot, cancel);
  expect(readJournal(slot).create).toEqual(ticket);
  expect(sessionStorage.getItem(slot)).not.toContain(p.name);
});
it('uses separate account-only original keys and a server-priced create body', async () => {
  const f = vi.fn().mockImplementation(async () => new Response('{}', { status: 200 }));
  vi.stubGlobal('fetch', f);
  await orderApi.submit(ticket);
  await orderApi.receipt(ticket);
  await orderApi.submit(cancel);
  await orderApi.receipt(cancel);
  await orderApi.list('cursor+');
  await orderApi.detail(o.id);
  const [a, b, c, d] = f.mock.calls;
  expect(JSON.parse(a[1].body)).toEqual(productIdentity(p));
  expect(JSON.parse(c[1].body)).toEqual({ expected_version: 1 });
  expect(c[1].headers['Idempotency-Key']).not.toBe(a[1].headers['Idempotency-Key']);
  expect(b[1].headers['Idempotency-Key']).toBe(a[1].headers['Idempotency-Key']);
  expect(d[1].headers['Idempotency-Key']).toBe(c[1].headers['Idempotency-Key']);
  for (const [path, init] of f.mock.calls) {
    expect(path).not.toMatch(/project_id|original-create-key|another-cancel-key/);
    expect(new Headers(init.headers).has('X-Project-ID')).toBe(false);
  }
  expect(f.mock.calls.map(([, i]) => i.method || 'GET')).toEqual([
    'POST',
    'GET',
    'POST',
    'GET',
    'GET',
    'GET',
  ]);
});
it('does not treat a different product, order or state as the original receipt', () => {
  expect(() => checkIntentReceipt(cancelled, ticket)).toThrow();
  expect(() =>
    checkIntentReceipt({ ...o, product: { ...p, product_digest: 'c'.repeat(64) } }, ticket),
  ).toThrow();
  expect(() => checkIntentReceipt({ ...cancelled, id: 'ord_' + 'c'.repeat(32) }, cancel)).toThrow();
});
it('does not expose actions or contact the private API in memory mode', async () => {
  const spy = vi.spyOn(orderApi, 'catalog');
  await render(false);
  expect(view.textContent).toContain('PostgreSQL');
  expect(spy).not.toHaveBeenCalled();
  expect(view.querySelector('button')).toBeNull();
});
it('supports existing records and explicit cancellation with the product catalog disabled', async () => {
  setup({ enabled: false, can_create: false, can_manage: true, items: [] }, [o]);
  const submit = vi.spyOn(orderApi, 'submit').mockResolvedValue(cancelled);
  await render();
  expect(button('登记购买意向')).toBeUndefined();
  await act(() => view.querySelector<HTMLButtonElement>('[data-intent-row] button')!.click());
  await click('取消这笔意向');
  expect(submit).not.toHaveBeenCalled();
  vi.mocked(orderApi.detail).mockResolvedValue(cancelled);
  await click('确认取消意向');
  expect(submit).toHaveBeenCalledTimes(1);
  expect(view.querySelector('[data-selected-order]')?.getAttribute('data-state')).toBe('CANCELLED');
});
it('queries a lost original receipt without posting, and shows current status independently', async () => {
  setup();
  const write = vi.spyOn(orderApi, 'submit').mockRejectedValue(new ApiError('network', 0, true));
  const receipt = vi
    .spyOn(orderApi, 'receipt')
    .mockRejectedValueOnce(new ApiError('absent', 404))
    .mockResolvedValueOnce(o);
  await render();
  await click('登记购买意向');
  await click('确认创建意向');
  const original = write.mock.calls[0][0];
  expect(readJournal(slot).pending).toEqual(original);
  await click('核对原操作收据');
  expect(button('登记购买意向')?.disabled).toBe(true);
  expect(readJournal(slot).pending).toEqual(original);
  vi.mocked(orderApi.detail).mockResolvedValue(cancelled);
  await click('核对原操作收据');
  expect(receipt.mock.calls.map(([t]) => t.key)).toEqual([original.key, original.key]);
  expect(write).toHaveBeenCalledTimes(1);
  expect(readJournal(slot).pending).toBeUndefined();
  expect(view.querySelector('[data-selected-order]')?.getAttribute('data-state')).toBe('CANCELLED');
  expect(view.querySelector('[data-receipt-state]')?.getAttribute('data-receipt-state')).toBe('PENDING');
});
it('refreshes a changed catalog and requires a fresh explicit review, leaving the rejected operation unrecorded', async () => {
  setup();
  const write = vi
    .spyOn(orderApi, 'submit')
    .mockRejectedValue(new ApiError('商品目录已调整', 409, false, 'ORDER_CATALOG_CHANGED'));
  await render();
  await click('登记购买意向');
  vi.mocked(orderApi.catalog).mockResolvedValue({ ...catalog, items: [{ ...p, amount_minor: '1500' }] });
  await click('确认创建意向');
  expect(write).toHaveBeenCalledTimes(1);
  expect(view.textContent).toContain('CNY 15.00');
  expect(button('确认创建意向')).toBeUndefined();
  expect(readJournal(slot)).toEqual({});
});

const paymentProjection = {
  order_id: o.id,
  currency: 'CNY',
  currency_exponent: 2,
  environment: 'test',
  payment_state: 'RECEIVED',
  refund_state: 'PARTIAL',
  credit_state: 'REVIEW',
  paid_minor: '1200',
  refunded_minor: '300',
  granted_credit_micros: '10000001',
  reversed_credit_micros: '0',
};
function jsonResponse(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), { status });
}
async function selectRow(id = o.id) {
  const b = view.querySelector<HTMLButtonElement>('[data-intent-row="' + id + '"] button');
  expect(b?.disabled).toBe(false);
  await act(() => b!.click());
}
function heldResponse() {
  let resolve!: (r: Response) => void;
  const promise = new Promise<Response>((done) => { resolve = done; });
  return { promise, resolve };
}

it('reads frozen money and posted CREDIT independently from the current catalog, in an account-only GET', async () => {
  setup({ ...catalog, items: [{ ...p, amount_minor: '1800', credit_micros: '5555555' }] }, [o]);
  const fetch = vi.fn().mockResolvedValue(jsonResponse(paymentProjection));
  vi.stubGlobal('fetch', fetch);
  await render();
  await selectRow();
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(view.querySelector('[data-payment-order]')).toBeNull();
  await click('查看资金记录');
  expect(fetch).toHaveBeenCalledTimes(1);
  const [url, init] = fetch.mock.calls[0];
  expect(url).toBe('/api/v1/order-intents/' + o.id + '/payment-status');
  expect(init.method || 'GET').toBe('GET');
  expect(init.body).toBeUndefined();
  expect(init.cache).toBe('no-store');
  expect(new Headers(init.headers).has('X-Project-ID')).toBe(false);
  expect(new Headers(init.headers).has('Idempotency-Key')).toBe(false);
  const area = view.querySelector('[data-payment-order]')?.textContent;
  for (const text of ['测试环境记录', '已记录收款 CNY 12.00', '已记录退款 CNY 3.00', '权益待核对', '已入账 10.000001 CREDIT', '已冲正 0.000000 CREDIT']) {
    expect(area).toContain(text);
  }
  expect(button('取消这笔意向')).toBeUndefined();
  expect(view.textContent).toContain('已有资金资料，此意向不可取消。');
  expect(area).not.toContain('18.00');
  expect(area).not.toContain('5.555555');
  expect(sessionStorage.getItem(slot) || '').not.toContain('10000001');
});

it.each([
  ['UNBOUND', 'NONE', 'NOT_ISSUED', '无已核实资金记录'],
  ['AWAITING', 'NONE', 'NOT_ISSUED', '尚无已核实收款记录'],
  ['REVIEW', 'PENDING_MATCH', 'REVIEW', '退款资料待匹配原款'],
  ['RECEIVED', 'FULL', 'REVIEW', '已核实全额退款'],
  ['REVIEW', 'REVIEW', 'REVIEW', '收款资料待核对'],
])('renders the %s/%s/%s server states without deriving them from the intent', async (pay, refund, creditState, label) => {
  setup(catalog, [o]);
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
    ...paymentProjection, payment_state: pay, refund_state: refund, credit_state: creditState,
    paid_minor: '0', refunded_minor: '0', granted_credit_micros: '0', environment: pay === 'UNBOUND' ? '' : 'live',
  })));
  await render();
  await selectRow();
  await click('查看资金记录');
  const area = view.querySelector('[data-payment-order]')?.textContent;
  expect(area).toContain(label);
  expect(!!button('取消这笔意向')).toBe(pay === 'UNBOUND' || pay === 'AWAITING');
  expect(area).not.toContain('待付款');
  expect(area).not.toContain('测试环境');
});

it.each([
  { order_id: 'ord_' + 'c'.repeat(32) },
  { currency_exponent: 3 },
  { currency: 'USD' },
  { refunded_minor: -1 },
  { paid_minor: '1.20' },
  { credit_state: 'OTHER' },
  { payment_state: 'constructor' },
  { environment: 'unknown' },
  { environment: '' },
  { payment_state: 'UNBOUND' },
  { payment_state: 'UNBOUND', environment: '', refund_state: 'NONE', credit_state:'NOT_ISSUED', refunded_minor: '0', granted_credit_micros: '0' },
])('withholds an unverified or mismatched projection %o', async (changed) => {
  setup(catalog, [o]);
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ ...paymentProjection, ...changed })));
  await render();
  await selectRow();
  await click('查看资金记录');
  expect(view.querySelector('[data-payment-order]')).toBeNull();
  expect(view.textContent).toContain('暂时无法核实');
  expect(view.textContent).not.toContain('无已核实资金记录');
});

it('discards a late response after switching orders, even when the server still delivered a successful result', async () => {
  const another = { ...o, id: 'ord_' + 'c'.repeat(32) };
  setup(catalog, [o, another]);
  vi.mocked(orderApi.detail).mockImplementation(async (id) => id === o.id ? o : another);
  const held = heldResponse();
  const f = vi.fn().mockReturnValueOnce(held.promise).mockResolvedValueOnce(jsonResponse({ ...paymentProjection, order_id: another.id }));
  vi.stubGlobal('fetch', f);
  await render();
  await selectRow();
  await click('查看资金记录');
  await selectRow(another.id);
  await click('查看资金记录');
  await act(() => held.resolve(jsonResponse(paymentProjection)));
  expect(view.querySelector('[data-payment-order]')?.getAttribute('data-payment-order')).toBe(another.id);
  expect(f).toHaveBeenCalledTimes(2);
});

it('does not apply the old account response or its authorization failure after reauthentication', async () => {
  setup(catalog, [o]);
  const held = heldResponse();
  vi.stubGlobal('fetch', vi.fn().mockReturnValue(held.promise));
  await render();
  await selectRow();
  await click('查看资金记录');
  vi.mocked(orderApi.list).mockResolvedValue({ items: [] });
  await act(() => root.render(<OrderIntentsPage {...props} user="other-user" />));
  await act(() => held.resolve(jsonResponse({ error: { code: 'AUTH_REQUIRED' } }, 401)));
  expect(view.textContent).not.toContain(o.id);
  expect(view.textContent).not.toContain('当前会话无法读取');
  expect(props.onExpired).not.toHaveBeenCalled();
});

it.each([401, 403, 404])('clears protected details and ignores older successful reads after a %s refresh', async (status) => {
  setup(catalog, [o]);
  const held = heldResponse();
  vi.stubGlobal('fetch', vi.fn().mockReturnValueOnce(held.promise));
  await render();
  await selectRow();
  await click('查看资金记录');
  vi.mocked(orderApi.catalog).mockRejectedValueOnce(new ApiError('denied', status));
  await click('刷新订单');
  expect(view.querySelector('[data-selected-order]')).toBeNull();
  await act(() => held.resolve(jsonResponse(paymentProjection)));
  expect(view.textContent).not.toContain('已核实部分退款');
  expect(view.querySelector('[data-payment-order]')).toBeNull();
  expect(view.textContent).toContain('当前会话无法读取');
});

it('clears previously read amounts if the projection itself later denies access', async () => {
  setup(catalog, [o]);
  vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(jsonResponse(paymentProjection)).mockResolvedValueOnce(
    jsonResponse({ error: { code: 'ORDER_INTENT_NOT_FOUND' } }, 404),
  ));
  await render();
  await selectRow();
  await click('查看资金记录');
  expect(view.textContent).toContain('已核实部分退款');
  await click('重新读取记录');
  expect(view.textContent).not.toContain('10.000001 CREDIT');
  expect(view.querySelector('[data-selected-order]')).toBeNull();
});

it('shows an attributable refund received before the original payment without implying it has matched', async () => {
  setup(catalog, [o]);
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
    ...paymentProjection, payment_state: 'AWAITING', paid_minor: '0', refund_state: 'PENDING_MATCH',
    refunded_minor: '300', credit_state: 'NOT_ISSUED', granted_credit_micros: '0',
  })));
  await render();
  await selectRow();
  await click('查看资金记录');
  expect(view.textContent).toContain('已记录退款 CNY 3.00');
  expect(view.textContent).toContain('退款资料待匹配原款');
  expect(view.textContent).toContain('尚无已核实收款记录');
  expect(button('取消这笔意向')).toBeUndefined();
});

it('withdraws a cancellation confirmation while fresh evidence is being read', async () => {
  setup(catalog, [o]);
  const held = heldResponse();
  vi.stubGlobal('fetch', vi.fn()
    .mockResolvedValueOnce(jsonResponse(unboundProjection(o.id)))
    .mockReturnValueOnce(held.promise));
  const writes = vi.spyOn(orderApi, 'submit');
  await render();
  await selectRow();
  await click('查看资金记录');
  await click('取消这笔意向');
  expect(button('确认取消意向')).toBeDefined();
  await click('重新读取记录');
  expect(button('确认取消意向')).toBeUndefined();
  expect(button('取消这笔意向')).toBeUndefined();
  await act(() => held.resolve(jsonResponse(paymentProjection)));
  expect(button('取消这笔意向')).toBeUndefined();
  expect(writes).not.toHaveBeenCalled();
});

it('states the money included in a disputed total, even when disputed records leave zero attributable', async () => {
  setup(catalog, [o]);
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
    ...paymentProjection, payment_state: 'REVIEW', refund_state: 'REVIEW',
    paid_minor: '0', refunded_minor: '300',
  })));
  await render();
  await selectRow();
  await click('查看资金记录');
  const area = view.querySelector('[data-payment-order]')?.textContent;
  expect(area).toContain('可归属的同币种收款 CNY 0.00');
  expect(area).toContain('可归属的同币种退款 CNY 3.00');
  expect(area).toContain('归属有争议或币种不同的金额未计入。');
  expect(area).toContain('已入账 10.000001 CREDIT');
  expect(area).not.toContain('已记录收款 CNY 0.00');
});

it('rechecks A after opening B and only enables cancellation on a successful latest read without evidence', async () => {
  const other = { ...o, id: 'ord_' + 'c'.repeat(32) };
  setup(catalog, [o, other]);
  vi.mocked(orderApi.detail).mockImplementation(async (id) => id === o.id ? o : other);
  const wait = heldResponse();
  const fetch = vi.fn()
    .mockResolvedValueOnce(jsonResponse(paymentProjection))
    .mockResolvedValueOnce(jsonResponse({ ...paymentProjection, order_id: other.id }))
    .mockReturnValueOnce(wait.promise);
  vi.stubGlobal('fetch', fetch);
  await render();
  await selectRow();
  await click('查看资金记录');
  expect(button('取消这笔意向')).toBeUndefined();
  await selectRow(other.id);
  await click('查看资金记录');
  expect(button('取消这笔意向')).toBeUndefined();
  await selectRow(o.id);
  expect(fetch).toHaveBeenCalledTimes(3);
  expect(button('取消这笔意向')).toBeUndefined();
  await act(() => wait.resolve(jsonResponse(paymentProjection)));
  expect(button('取消这笔意向')).toBeUndefined();
});

it('enables the legacy cancellation confirmation after an explicit selection check recovers, and never posts while checking', async () => {
  setup({ enabled: false, can_create: false, can_manage: true, items: [] }, [o]);
  const held = heldResponse();
  const read = vi.fn().mockResolvedValueOnce(jsonResponse({}, 503)).mockReturnValueOnce(held.promise);
  vi.stubGlobal('fetch', read);
  const writes = vi.spyOn(orderApi, 'submit');
  await render();
  await selectRow();
  expect(button('取消这笔意向')).toBeUndefined();
  await click('查看资金记录');
  expect(button('取消这笔意向')).toBeUndefined();
  await act(() => held.resolve(jsonResponse(unboundProjection(o.id))));
  expect(button('取消这笔意向')?.disabled).toBe(false);
  await click('取消这笔意向');
  expect(button('确认取消意向')).toBeDefined();
  expect(writes).not.toHaveBeenCalled();
});
