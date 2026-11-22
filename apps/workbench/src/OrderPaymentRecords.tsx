import { uiText } from './locale';
import { useLayoutEffect, useRef, useState } from 'react';
import { ApiError, request } from './api';
import { fixedUnits } from './orderIntents';
import type { Order } from './orderIntents';

const payments = {
  get UNBOUND() { return uiText('无已核实资金记录'); },
  get AWAITING() { return uiText('已关联渠道，尚无已核实收款记录'); },
  get RECEIVED() { return uiText('已核实收款'); },
  get REVIEW() { return uiText('收款资料待核对'); },
};
const refunds = {
  get NONE() { return uiText('暂无退款记录'); },
  get PENDING_MATCH() { return uiText('退款资料待匹配原款'); },
  get PARTIAL() { return uiText('已核实部分退款'); },
  get FULL() { return uiText('已核实全额退款'); },
  get REVIEW() { return uiText('退款资料待核对'); },
};
const credits = {
  get NOT_ISSUED() { return uiText('尚无权益入账记录'); },
  get ISSUED() { return uiText('权益已入账'); },
  get REVIEW() { return uiText('权益待核对'); },
};

export type PaymentProjection = {
  order_id: string;
  currency: string;
  currency_exponent: number;
  environment: '' | 'test' | 'live';
  payment_state: keyof typeof payments;
  refund_state: keyof typeof refunds;
  credit_state: keyof typeof credits;
  paid_minor: string;
  refunded_minor: string;
  granted_credit_micros: string;
  reversed_credit_micros: string;
};

export async function readOrderPayment(order: Order, signal: AbortSignal) {
  const p = await request<PaymentProjection>(
    '/order-intents/' + encodeURIComponent(order.id) + '/payment-status',
    {
      signal,
      cache: 'no-store',
    },
  );
  const validUnits = (raw: unknown) => typeof raw === 'string' && /^(0|[1-9][0-9]{0,31})$/.test(raw);
  if (
    !p ||
    p.order_id !== order.id ||
    p.currency !== order.product.currency ||
    p.currency_exponent !== order.product.currency_exponent ||
    !Object.hasOwn(payments, p.payment_state) ||
    !Object.hasOwn(refunds, p.refund_state) ||
    !Object.hasOwn(credits, p.credit_state)
  ) {
    throw Error(uiText('订单、币种或记录状态无法核实。'));
  }
  const amounts = [p.paid_minor, p.refunded_minor, p.granted_credit_micros, p.reversed_credit_micros];
  if (!amounts.every(validUnits)) throw Error(uiText('记录金额无法核实。'));
  if (p.payment_state === 'UNBOUND') {
    if (
      p.environment !== '' ||
      p.refund_state !== 'NONE' ||
      p.credit_state !== 'NOT_ISSUED' ||
      amounts.some((value) => value !== '0')
    ) {
      throw Error(uiText('未关联意向的资金记录不一致。'));
    }
  } else if (p.environment !== 'test' && p.environment !== 'live') {
    throw Error(uiText('资金记录来源无法核实。'));
  }
  return p;
}

export type CancellationCheck = 'unknown' | 'available' | 'recorded';

type Props = {
  order: Order;
  onDenied: (e: ApiError) => void;
  onCheck: (state: CancellationCheck) => void;
};
export function OrderPaymentRecords({ order, onDenied, onCheck }: Props) {
  const [record, setRecord] = useState<PaymentProjection | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const pending = useRef<AbortController | null>(null);
  // The parent remounts this section for every explicit selection. One GET
  // supplies both the cancellation check and the details the user can expand.
  useLayoutEffect(() => {
    void read();
    return () => {
      pending.current?.abort();
      pending.current = null;
    };
  }, []);
  async function read() {
    if (pending.current) return;
    const controller = new AbortController();
    pending.current = controller;
    onCheck('unknown');
    setBusy(true);
    setError('');
    setRecord(null);
    try {
      const result = await readOrderPayment(order, controller.signal);
      if (controller.signal.aborted) return;
      setRecord(result);
      const cancellable =
        (result.payment_state === 'UNBOUND' || result.payment_state === 'AWAITING') &&
        result.refund_state === 'NONE' && result.credit_state === 'NOT_ISSUED' &&
        [result.paid_minor, result.refunded_minor, result.granted_credit_micros, result.reversed_credit_micros]
          .every((amount) => amount === '0');
      onCheck(cancellable ? 'available' : 'recorded');
    } catch (e) {
      if (controller.signal.aborted) return;
      if (e instanceof ApiError && [401, 403, 404].includes(e.status)) {
        onDenied(e);
      } else {
        setError(uiText('暂时无法核实这笔订单的记录，请重新读取。'));
      }
    } finally {
      if (!controller.signal.aborted) {
        pending.current = null;
        setBusy(false);
      }
    }
  }
  const cash = (raw: string) =>
    order.product.currency + ' ' + fixedUnits(raw, order.product.currency_exponent);
  const points = (raw: string) => fixedUnits(raw, 6) + ' CREDIT';
  return (
    <section className="order-payments" aria-label={uiText("资金和权益记录")} aria-busy={busy}>
      <div className="intent-section-head">
        <h3>{uiText("资金和权益记录")}</h3>
        <button disabled={expanded && busy} onClick={() => {
          setExpanded(true);
          if (expanded || error) void read();
        }}>
          {!expanded ? uiText('查看资金记录') : busy ? uiText('正在读取…') : uiText('重新读取记录')}
        </button>
      </div>
      {error && <p role="alert">{error}</p>}
      {expanded && record && (
        <div data-payment-order={record.order_id}>
          {record.environment && (
            <p className="order-source">{record.environment === 'test' ? uiText('测试环境记录') : uiText('正式环境记录')}</p>
          )}
          <dl className="order-entries">
            <div data-payment-state={record.payment_state}>
              <dt>{uiText("收款")}</dt>
              <dd>
                <strong>{payments[record.payment_state]}</strong>
                <span>{record.payment_state === 'REVIEW' ? uiText('可归属的同币种收款') : uiText('已记录收款')} {cash(record.paid_minor)}</span>
              </dd>
            </div>
            <div data-refund-state={record.refund_state}>
              <dt>{uiText("退款")}</dt>
              <dd>
                <strong>{refunds[record.refund_state]}</strong>
                <span>{record.refund_state === 'REVIEW' ? uiText('可归属的同币种退款') : uiText('已记录退款')} {cash(record.refunded_minor)}</span>
              </dd>
            </div>
            <div data-credit-state={record.credit_state}>
              <dt>{uiText("创作权益")}</dt>
              <dd>
                <strong>{credits[record.credit_state]}</strong>
                <span>{uiText("已入账")} {points(record.granted_credit_micros)}</span>
                <span>{uiText("已冲正")} {points(record.reversed_credit_micros)}</span>
              </dd>
            </div>
          </dl>
          {(record.payment_state === 'REVIEW' || record.refund_state === 'REVIEW') && (
            <p>{uiText("待核金额按本订单的币种汇总，归属有争议或币种不同的金额未计入。")}</p>
          )}
        </div>
      )}
    </section>
  );
}
