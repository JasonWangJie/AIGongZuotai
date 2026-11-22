import { uiText } from './locale';
import { request } from './api';

export type Product = {
  id: string;
  name: string;
  currency: string;
  currency_exponent: number;
  amount_minor: string;
  credit_micros: string;
  catalog_version: string;
  catalog_digest: string;
  product_digest: string;
};
export type Catalog = {
  enabled: boolean;
  can_create: boolean;
  can_manage: boolean;
  items: Product[];
  catalog_version?: string;
  catalog_digest?: string;
};
export type Order = {
  id: string;
  product: Product;
  state: 'PENDING' | 'CANCELLED';
  version: number;
  created_at: string;
  cancelled_at?: string;
};
export type OrderPage = { items: Order[]; next_cursor?: string | null };
type ProductIdentity = { product_id: string; catalog_digest: string; product_digest: string };
export type IntentTicket = (
  { operation: 'create'; product: ProductIdentity } | { operation: 'cancel'; id: string; version: number }
) & { key: string };
export type IntentJournal = { pending?: IntentTicket; create?: IntentTicket; cancel?: IntentTicket };

const positive = (value: unknown, max: string) =>
  typeof value === 'string' &&
  /^[1-9][0-9]*$/.test(value) &&
  (value.length < max.length || (value.length === max.length && value <= max));
const sha = (s: unknown) => typeof s === 'string' && /^[a-f0-9]{64}$/.test(s);
const slug = (s: unknown) => typeof s === 'string' && /^[a-z][a-z0-9_-]{0,47}$/.test(s);
const id = (s: unknown) => typeof s === 'string' && /^ord_[a-f0-9]{32}$/.test(s);
const exponents: Record<string, number> = { CNY: 2, USD: 2, EUR: 2, HKD: 2, GBP: 2, JPY: 0, KWD: 3 };
export function fixedUnits(raw: string, scale: number) {
  if (!/^(0|[1-9][0-9]*)$/.test(raw) || !Number.isInteger(scale) || scale < 0 || scale > 6)
    throw Error(uiText('金额精度无法识别。'));
  if (scale === 0) return raw;
  const padded = raw.padStart(scale + 1, '0');
  return padded.slice(0, -scale) + '.' + padded.slice(-scale);
}
export const money = (p: Product) => p.currency + ' ' + fixedUnits(p.amount_minor, p.currency_exponent);
export const credit = (p: Product) => fixedUnits(p.credit_micros, 6) + ' CREDIT';
export const stateLabel = (state: Order['state']) => (state === 'PENDING' ? uiText('待处理') : uiText('已取消'));
function validProduct(p: Product) {
  return (
    p &&
    slug(p.id) &&
    typeof p.name === 'string' &&
    !!p.name.trim() &&
    /^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$/.test(p.catalog_version) &&
    sha(p.catalog_digest) &&
    sha(p.product_digest) &&
    Object.hasOwn(exponents, p.currency) &&
    exponents[p.currency] === p.currency_exponent &&
    positive(p.amount_minor, '1000000000000') &&
    positive(p.credit_micros, '1000000000000000')
  );
}
export function checkCatalog(c: Catalog) {
  if (
    !c ||
    [c.enabled, c.can_create, c.can_manage].some((v) => typeof v !== 'boolean') ||
    (c.can_create && (!c.enabled || !c.can_manage)) ||
    !Array.isArray(c.items) ||
    c.items.length > 32 ||
    new Set(c.items.map((p) => p?.id)).size !== c.items.length ||
    !c.items.every(
      (p) =>
        validProduct(p) && p.catalog_digest === c.catalog_digest && p.catalog_version === c.catalog_version,
    )
  )
    throw Error(uiText('服务端商品目录不完整，请刷新核对。'));
  return c;
}
export function checkOrder(o: Order, expectedID?: string) {
  if (
    !o ||
    !id(o.id) ||
    (expectedID && o.id !== expectedID) ||
    !validProduct(o.product) ||
    !Number.isFinite(Date.parse(o.created_at)) ||
    !(
      (o.state === 'PENDING' && o.version === 1) ||
      (o.state === 'CANCELLED' && o.version === 2 && Number.isFinite(Date.parse(o.cancelled_at || '')))
    )
  )
    throw Error(uiText('订单记录或版本无法核实。'));
  return o;
}
export const productIdentity = (p: Product): ProductIdentity => ({
  product_id: p.id,
  catalog_digest: p.catalog_digest,
  product_digest: p.product_digest,
});
export function checkIntentReceipt(o: Order, t: IntentTicket) {
  checkOrder(o, t.operation === 'cancel' ? t.id : undefined);
  if (
    t.operation === 'create'
      ? o.state !== 'PENDING' ||
        o.product.id !== t.product.product_id ||
        o.product.product_digest !== t.product.product_digest ||
        o.product.catalog_digest !== t.product.catalog_digest
      : o.state !== 'CANCELLED' || o.version !== t.version + 1
  )
    throw Error(uiText('回执与原商品、订单或操作版本不符。'));
  return o;
}
const base = '/order-intents';
const key = (t: IntentTicket) => ({ 'Idempotency-Key': t.key });
export const orderApi = {
  catalog: () => request<Catalog>(base + '/catalog'),
  list: (cursor?: string) =>
    request<OrderPage>(base + '?limit=20' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : '')),
  detail: (order: string) => request<Order>(base + '/' + encodeURIComponent(order)),
  submit: (t: IntentTicket) =>
    t.operation === 'create'
      ? request<Order>(base, { method: 'POST', headers: key(t), body: JSON.stringify(t.product) })
      : request<Order>(base + '/' + encodeURIComponent(t.id) + '/cancel', {
          method: 'POST',
          headers: key(t),
          body: JSON.stringify({ expected_version: t.version }),
        }),
  receipt: (t: IntentTicket) =>
    request<Order>(base + '/receipt?operation=' + t.operation, { headers: key(t) }),
};
export const intentSlot = (user: string, workspace: string) =>
  'xingtu-order-intent:' + JSON.stringify([user, workspace]);
function validTicket(t: IntentTicket) {
  return (
    t &&
    typeof t.key === 'string' &&
    /^[-\w]{12,100}$/.test(t.key) &&
    (t.operation === 'create'
      ? t.product &&
        slug(t.product.product_id) &&
        sha(t.product.catalog_digest) &&
        sha(t.product.product_digest)
      : t.operation === 'cancel' && id(t.id) && t.version === 1)
  );
}
export function readJournal(slot: string): IntentJournal {
  const raw = sessionStorage.getItem(slot);
  if (raw === null) return {};
  const j = JSON.parse(raw) as IntentJournal;
  if (
    !j ||
    typeof j !== 'object' ||
    Array.isArray(j) ||
    Object.entries(j).some(
      ([name, t]) =>
        !['pending', 'create', 'cancel'].includes(name) ||
        !validTicket(t) ||
        (name !== 'pending' && name !== t.operation),
    )
  )
    throw Error(uiText('本页原操作编号无法读取，请核对本人记录。'));
  return j;
}
export function beginTicket(slot: string, t: IntentTicket) {
  const j = readJournal(slot);
  if (j.pending) throw Error(uiText('先核对上次操作的原收据。'));
  const next = { ...j, pending: t };
  sessionStorage.setItem(slot, JSON.stringify(next));
  return next;
}
export function finishTicket(slot: string, t: IntentTicket, accepted: boolean) {
  const j = readJournal(slot);
  if (j.pending?.key !== t.key) return j;
  delete j.pending;
  if (accepted) j[t.operation] = t;
  sessionStorage.setItem(slot, JSON.stringify(j));
  return j;
}
