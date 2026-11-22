import { uiText, currentLocale } from './locale';
import { useEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import { OrderPaymentRecords, type CancellationCheck } from './OrderPaymentRecords';
import {
  beginTicket,
  checkCatalog,
  checkIntentReceipt,
  checkOrder,
  credit,
  finishTicket,
  intentSlot,
  money,
  orderApi,
  productIdentity,
  readJournal,
  stateLabel,
} from './orderIntents';
import type { Catalog, IntentJournal, IntentTicket, Order, Product } from './orderIntents';
import './order-intents.css';

type Props = { user: string; workspace: string; enabled: boolean; onExpired: () => void };
type Choice = { operation: 'create'; product: Product } | { operation: 'cancel'; order: Order };
type Receipt = { ticket: IntentTicket; order: Order };
function Figures({ p }: { p: Product }) {
  return (
    <dl className="intent-figures">
      <div>
        <dt>{uiText("商品标价")}</dt>
        <dd>{money(p)}</dd>
      </div>
      <div>
        <dt>{uiText("拟购创作权益")}</dt>
        <dd>{credit(p)}</dd>
      </div>
    </dl>
  );
}
const date = (s: string) => new Date(s).toLocaleString(currentLocale(), { hour12: false });
export function OrderIntentsPage(props: Props) {
  return <OrderSession key={JSON.stringify([props.user, props.workspace, props.enabled])} {...props} />;
}

function OrderSession(props: Props) {
  const [denied, setDenied] = useState(false);
  if (denied) {
    return (
      <section className="intent-work" aria-label={uiText("本人订单")}>
        <h1>{uiText("本人订单")}</h1>
        <p role="alert">{uiText("当前会话无法读取这笔订单。")}</p>
        <button onClick={() => setDenied(false)}>{uiText("重新读取本人订单")}</button>
      </section>
    );
  }
  return <AccountOrders {...props} onDenied={(e) => {
    setDenied(true);
    if (e.status === 401 || e.code === 'ACCOUNT_RESTRICTED') props.onExpired();
  }} />;
}

function AccountOrders({ user, workspace, enabled, onDenied }: Props & { onDenied(e: ApiError): void }) {
  const slot = intentSlot(user, workspace);
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [rows, setRows] = useState<Order[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [selected, setSelected] = useState<Order | null>(null);
  const [receipt, setReceipt] = useState<Receipt | null>(null);
  const [cancelCheck, setCancelCheck] = useState<CancellationCheck>('unknown');
  const [selectionVersion, setSelectionVersion] = useState(0);
  const [choice, setChoice] = useState<Choice | null>(null);
  const [journal, setJournal] = useState<IntentJournal>({});
  const [blocked, setBlocked] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [ready, setReady] = useState(false);
  const live = useRef(true),
    locking = useRef(false),
    epoch = useRef(0);
  function failure(e: unknown) {
    if (!live.current) return;
    if (e instanceof ApiError && [401, 403, 404].includes(e.status)) {
      live.current = false;
      ++epoch.current;
      onDenied(e);
      return;
    }
    setErr(e instanceof Error ? e.message : uiText('订单记录暂时无法读取。'));
  }
  async function loadRows(after?: string, run = epoch.current) {
    const page = await orderApi.list(after);
    if (!Array.isArray(page.items)) throw Error(uiText('订单列表无法读取。'));
    const items = page.items.map((o) => checkOrder(o));
    if (!live.current || epoch.current !== run) return;
    setRows((old) => {
      const merged = new Map((after ? old : []).map((o) => [o.id, o]));
      for (const o of items) {
        const existing = merged.get(o.id);
        if (!existing || existing.version <= o.version) merged.set(o.id, o);
      }
      return [...merged.values()];
    });
    setCursor(page.next_cursor || null);
  }
  async function sync(run: number) {
    const c = checkCatalog(await orderApi.catalog());
    if (!live.current || epoch.current !== run) return;
    setCatalog(c);
    if (c.can_manage) await loadRows(undefined, run);
    else {
      setRows([]);
      setSelected(null);
      setReceipt(null);
      setCursor(null);
    }
    if (live.current && epoch.current === run) setReady(true);
  }
  useEffect(() => {
    live.current = true;
    const run = ++epoch.current;
    if (enabled) {
      try {
        setJournal(readJournal(slot));
      } catch (e) {
        setBlocked(true);
        failure(e);
      }
      void sync(run).catch((e) => {
        if (epoch.current === run) failure(e);
      });
    }
    return () => {
      live.current = false;
      ++epoch.current;
    };
  }, [slot, enabled]);
  async function handle(fn: () => Promise<void>) {
    if (locking.current) return;
    locking.current = true;
    setBusy(true);
    setErr('');
    try {
      await fn();
    } catch (e) {
      failure(e);
    } finally {
      locking.current = false;
      if (live.current) setBusy(false);
    }
  }
  async function current(order: string) {
    // Changing the selection invalidates any outstanding private read for the previous order.
    const run = epoch.current;
    setCancelCheck('unknown');
    setSelected(null);
    const now = checkOrder(await orderApi.detail(order), order);
    if (!live.current || epoch.current !== run) return;
    setSelectionVersion((version) => version + 1);
    setSelected(now);
    setRows((old) => old.map((o) => (o.id === now.id && o.version <= now.version ? now : o)));
  }
  async function showReceipt(t: IntentTicket, o: Order) {
    checkIntentReceipt(o, t);
    const j = finishTicket(slot, t, true);
    if (!live.current) return;
    setJournal(j);
    setReceipt({ ticket: t, order: o });
    setChoice(null);
    // A create receipt stays at v1 after cancellation. Fetch the current record independently.
    setSelected(null);
    await current(o.id);
    if (live.current) await loadRows();
  }
  async function submit() {
    if (!choice || journal.pending || blocked || !catalog?.can_manage) return;
    if (choice.operation === 'cancel' && (cancelCheck !== 'available' || selected?.id !== choice.order.id)) return;
    const t: IntentTicket =
      choice.operation === 'create'
        ? { operation: 'create', key: crypto.randomUUID(), product: productIdentity(choice.product) }
        : {
            operation: 'cancel',
            key: crypto.randomUUID(),
            id: choice.order.id,
            version: choice.order.version,
          };
    if (t.operation === 'create' && !catalog.can_create) return;
    setJournal(beginTicket(slot, t));
    setChoice(null);
    let response: Order;
    try {
      response = await orderApi.submit(t);
    } catch (e) {
      if (e instanceof ApiError && !e.uncertain) {
        const j = finishTicket(slot, t, false);
        if (live.current) setJournal(j);
        if (live.current && e.status === 409) {
          await sync(++epoch.current);
          if (t.operation === 'cancel') await current(t.id);
          if (t.operation === 'cancel' && e.code === 'ORDER_PAYMENT_RECORDED') {
            setCancelCheck('recorded');
            throw Error(uiText('这笔意向已有资金资料，无法取消。请查看资金和权益记录。'));
          }
        }
        throw e;
      }
      throw Error(uiText('回执尚未确认。请使用原操作编号核对。'));
    }
    await showReceipt(t, response);
  }
  async function query(t: IntentTicket) {
    let o: Order;
    try {
      o = await orderApi.receipt(t);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404)
        throw Error(uiText('暂未查到原收据。可刷新本人记录，稍后再次核对。'));
      throw e;
    }
    await showReceipt(t, o);
  }
  const pending = journal.pending,
    lock = busy || !!pending || blocked;
  if (!enabled)
    return (
      <section className="intent-work">
        <header>
          <h1>{uiText("本人订单")}</h1>
        </header>
        <p>{uiText("订单意向需要启用 PostgreSQL 和账户登录。")}</p>
      </section>
    );
  const product = choice?.operation === 'create' ? choice.product : choice?.order.product;
  return (
    <section className="intent-work" aria-label={uiText("本人订单")}>
      <header className="intent-head">
        <div>
          <h1>{uiText("本人订单")}</h1>
          <p>{uiText("查看商品标价、登记购买意向，并核对自己的订单记录。")}</p>
        </div>
        <button
          disabled={busy || !!choice}
          onClick={() =>
            void handle(async () => {
              await sync(++epoch.current);
              if (selected) await current(selected.id);
            })
          }
        >{uiText("刷新订单")}</button>
      </header>
      <p className="intent-scope">{uiText("登记或取消意向均不收款，也不发放 CREDIT。可在订单详情核对已记录的收款、退款与权益变动。")}</p>
      {err && (
        <p className="intent-warning" role="alert">
          {err}
        </p>
      )}
      {pending && (
        <div className="intent-note" role="status">
          <strong>{pending.operation === 'create' ? uiText('创建意向') : uiText('取消意向')}{uiText("待核对")}</strong>
          <p>{uiText("请用原操作编号查询对应收据。")}</p>
          <button disabled={busy} onClick={() => void handle(() => query(pending))}>{uiText("核对原操作收据")}</button>
        </div>
      )}
      {choice && product && (
        <section className="intent-confirm" aria-label={uiText("确认意向")}>
          <h2>{choice.operation === 'create' ? uiText('核对购买意向') : uiText('核对取消意向')}</h2>
          <strong>{product.name}</strong>
          <Figures p={product} />
          <p>
            {choice.operation === 'create'
              ? uiText('将按目录 ') + product.catalog_version + uiText(' 中的商品和标价登记。')
              : uiText('将取消订单 ') +
                (choice.operation === 'cancel' ? choice.order.id : '') +
                uiText('。历史记录仍可查看。')}
          </p>
          <div className="intent-actions">
            <button className="intent-primary" disabled={lock} onClick={() => void handle(submit)}>
              {choice.operation === 'create' ? uiText('确认创建意向') : uiText('确认取消意向')}
            </button>
            <button disabled={busy} onClick={() => setChoice(null)}>{uiText("返回")}</button>
          </div>
        </section>
      )}
      <section aria-label={uiText("商品目录")}>
        <div className="intent-section-head">
          <h2>{uiText("商品目录")}</h2>
          {catalog?.enabled && <small>{uiText("版本")} {catalog.catalog_version}</small>}
        </div>
        {!catalog ? (
          <p>{uiText("正在读取商品目录…")}</p>
        ) : !catalog.can_create ? (
          <p>{uiText("当前未开放新意向登记。")}{catalog.can_manage && uiText('已登记的意向可继续查看和取消。')}</p>
        ) : (
          <div className="intent-catalog">
            {catalog.items.map((p) => (
              <article key={p.id} data-intent-product={p.id}>
                <h3>{p.name}</h3>
                <Figures p={p} />
                <button
                  disabled={lock || !!choice}
                  onClick={() => {
                    setErr('');
                    setChoice({ operation: 'create', product: p });
                  }}
                >{uiText("登记购买意向")}</button>
              </article>
            ))}
          </div>
        )}
      </section>
      {catalog?.can_manage && (
        <section aria-label={uiText("订单记录")}>
          <div className="intent-section-head">
            <h2>{uiText("我的意向记录")}</h2>
          </div>
          {rows.length > 0 ? (
            <ul className="intent-list">
              {rows.map((o) => (
                <li key={o.id} data-intent-row={o.id}>
                  <button disabled={busy || !!choice} onClick={() => void handle(() => current(o.id))}>
                    <span>
                      <strong>{o.product.name}</strong>
                      <small>{date(o.created_at)}</small>
                    </span>
                    <span>{money(o.product)}</span>
                    <span className="intent-state" data-state={o.state}>
                      {stateLabel(o.state)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p>{ready ? uiText('当前账户还没有订单意向。') : uiText('正在读取本人记录…')}</p>
          )}
          {cursor && (
            <button disabled={busy} onClick={() => void handle(() => loadRows(cursor))}>{uiText("加载更多订单")}</button>
          )}
        </section>
      )}
      {selected && (
        <section
          className="intent-detail"
          aria-label={uiText("当前订单")}
          data-selected-order={selected.id}
          data-state={selected.state}
        >
          <div className="intent-section-head">
            <h2>{uiText("当前订单")}</h2>
            <strong className="intent-state" data-state={selected.state}>
              {stateLabel(selected.state)}
            </strong>
          </div>
          <h3>{selected.product.name}</h3>
          <Figures p={selected.product} />
          <dl className="intent-meta">
            <div>
              <dt>{uiText("订单编号")}</dt>
              <dd>{selected.id}</dd>
            </div>
            <div>
              <dt>{uiText("原商品版本")}</dt>
              <dd>{selected.product.catalog_version}</dd>
            </div>
            <div>
              <dt>{uiText("登记时间")}</dt>
              <dd>{date(selected.created_at)}</dd>
            </div>
            {selected.cancelled_at && (
              <div>
                <dt>{uiText("取消时间")}</dt>
                <dd>{date(selected.cancelled_at)}</dd>
              </div>
            )}
          </dl>
          <OrderPaymentRecords
            key={selectionVersion}
            order={selected}
            onDenied={failure}
            onCheck={(state) => {
              setCancelCheck(state);
              if (state !== 'available') setChoice((old) => old?.operation === 'cancel' ? null : old);
            }}
          />
          {selected.state === 'PENDING' && cancelCheck === 'recorded' && (
            <p>{uiText("已有资金资料，此意向不可取消。")}</p>
          )}
          {selected.state === 'PENDING' && cancelCheck === 'available' && (
            <button
              disabled={lock || !!choice}
              onClick={() => {
                setErr('');
                setChoice({ operation: 'cancel', order: selected });
              }}
            >{uiText("取消这笔意向")}</button>
          )}
        </section>
      )}
      {catalog?.can_manage && (journal.create || journal.cancel || receipt) && (
        <section aria-label={uiText("操作收据")}>
          <div className="intent-section-head">
            <h2>{uiText("操作收据")}</h2>
          </div>
          <div className="intent-actions">
            {(['create', 'cancel'] as const).map((op) => {
              const t = journal[op];
              return (
                t && (
                  <button key={op} disabled={lock || !!choice} onClick={() => void handle(() => query(t))}>
                    {op === 'create' ? uiText('查看上次创建收据') : uiText('查看上次取消收据')}
                  </button>
                )
              );
            })}
          </div>
          {receipt && (
            <p
              className="intent-receipt"
              role="status"
              data-receipt-operation={receipt.ticket.operation}
              data-receipt-state={receipt.order.state}
            >
              {receipt.ticket.operation === 'create' ? uiText('创建') : uiText('取消')}{uiText("收据 · 操作当时")}{stateLabel(receipt.order.state)} · {receipt.order.id}
            </p>
          )}
        </section>
      )}
    </section>
  );
}
