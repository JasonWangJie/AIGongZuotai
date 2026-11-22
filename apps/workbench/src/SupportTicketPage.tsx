import { uiText, currentLocale } from './locale';
import { useEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import {
  categories,
  checkedPage,
  checkedTicket,
  checkedReceipt,
  forgetOriginal,
  keepOriginal,
  kinds,
  operations,
  readOriginal,
  supportApi,
  validText,
} from './supportTickets';
import type { Actor, Card, Category, Op, Original, Ticket } from './supportTickets';
import './support-tickets.css';

type Props = {
  user: string;
  workspace: string;
  staff: boolean;
  grants: string[];
  onExpired: () => void;
  onVerifyAccess: () => void;
};
export function SupportTicketPage({ user, workspace, staff, grants, onExpired, onVerifyAccess }: Props) {
  const [preferred, setPreferred] = useState('');
  const [scopeLocked, setScopeLocked] = useState(false);
  const target = staff ? (grants.includes(preferred) ? preferred : grants[0]) : workspace;
  if (!target)
    return (
      <section className="support-page">
        <h1>{uiText("客服收件箱")}</h1>
        <p>{uiText("当前没有已授权的客服工作区。")}</p>
        <button onClick={onVerifyAccess}>{uiText("核对客服权限")}</button>
      </section>
    );
  return (
    <section className="support-page">
      <header>
        <h1>{staff ? uiText('客服收件箱') : uiText('我的客服工单')}</h1>
        <p>{staff ? uiText('在获授权的工作区阅读工单并回复用户。') : uiText('向工作区客服说明问题，查看答复并继续追问。')}</p>
      </header>
      {staff && (
        <div className="support-scope">
          <label>{uiText("客服工作区")}<select value={target} disabled={scopeLocked} onChange={(e) => setPreferred(e.target.value)}>
              {grants.map((w) => (
                <option key={w}>{w}</option>
              ))}
            </select>
          </label>
          <button disabled={scopeLocked} onClick={onVerifyAccess}>{uiText("核对客服权限")}</button>
        </div>
      )}
      <TicketDesk
        key={JSON.stringify([user, target, staff])}
        actor={{ user, workspace: target, staff }}
        onExpired={onExpired}
        onScopeLocked={setScopeLocked}
      />
    </section>
  );
}
const formatTime = (v: string) => new Date(v).toLocaleString(currentLocale(), { hour12: false });
const stateTitle = (state: Card['state']) => (state === 'OPEN' ? uiText('处理中') : uiText('已关闭'));
function TicketDesk({
  actor,
  onExpired,
  onScopeLocked,
}: {
  actor: Actor;
  onExpired: () => void;
  onScopeLocked: (value: boolean) => void;
}) {
  const [rows, setRows] = useState<Card[]>([]),
    [cursor, setCursor] = useState<string | null>(null);
  const [current, setCurrent] = useState<Ticket | null>(null),
    [baseVersion, setBaseVersion] = useState(0);
  const [category, setCategory] = useState<Category>('account'),
    [subject, setSubject] = useState(''),
    [description, setDescription] = useState(''),
    [reply, setReply] = useState('');
  const [pending, setPending] = useState<Original | null>(null),
    [last, setLast] = useState<{ op: Op; version: number } | null>(null);
  const [ready, setReady] = useState(false),
    [blocked, setBlocked] = useState(false),
    [busy, setBusy] = useState(false),
    [confirmClose, setConfirmClose] = useState(false),
    [error, setError] = useState('');
  const composer = useRef<HTMLDetailsElement>(null);
  const pw = useRef<HTMLInputElement>(null),
    alive = useRef(true),
    locked = useRef(false),
    counter = useRef(0),
    accessVersion = useRef(0);
  const value = useRef({ id: '', dirty: false });
  value.current = { id: current?.id || '', dirty: !!reply };
  useEffect(() => {
    onScopeLocked(busy || !!reply);
    return () => onScopeLocked(false);
  }, [busy, reply, onScopeLocked]);
  function clearPrivate() {
    // Older authorized responses can still be in flight when a denial arrives.
    ++accessVersion.current;
    ++counter.current;
    setRows([]);
    setCursor(null);
    setCurrent(null);
    setBaseVersion(0);
    setDescription('');
    setSubject('');
    setReply('');
    setReady(false);
    setLast(null);
    setConfirmClose(false);
    if (pw.current) pw.current.value = '';
  }
  function problem(e: unknown) {
    if (!alive.current) return;
    if (e instanceof ApiError) {
      if (e.status === 401) {
        clearPrivate();
        onExpired();
        return;
      }
      if (e.code === 'OPERATIONS_PASSWORD_INVALID' || e.code === 'INVALID_OPERATIONS_PASSWORD') {
        setError(uiText('请核对当前客服账户的密码并重新输入。'));
        return;
      }
      if (
        e.status === 403 ||
        e.status === 404 ||
        e.code === 'OPERATIONS_DISABLED' ||
        e.code === 'SUPPORT_STORAGE_REQUIRED'
      ) {
        clearPrivate();
        setError(uiText('当前无法读取该工作区或工单，请核对访问权限。'));
        return;
      }
      const messages: Record<string, string> = {
        SUPPORT_TICKET_CHANGED: uiText('工单已更新。请阅读最新消息后再确认本次操作。'),
        SUPPORT_TICKET_MESSAGE_LIMIT: uiText('工单已达到消息上限，可关闭后另行建单。'),
        SUPPORT_TICKET_OPEN_LIMIT: uiText('当前已有八个开放工单，请先处理已有工单。'),
      };
      setError(messages[e.code || ''] || uiText('本次操作未完成，请核对工单状态。'));
      return;
    }
    setError(e instanceof Error ? e.message : uiText('工单操作未完成。'));
  }
  async function list(next?: string) {
    const n = ++counter.current;
    const page = checkedPage(await supportApi.list(actor, next), actor.workspace);
    if (!alive.current || n !== counter.current) return;
    setRows((old) =>
      next ? [...new Map([...old, ...page.items].map((r) => [r.id, r])).values()] : page.items,
    );
    setCursor(page.next_cursor);
    setReady(true);
  }
  async function detail(id: string, discard = false) {
    const started = accessVersion.current;
    const t = checkedTicket(await supportApi.detail(actor, id), actor.workspace, id);
    if (!alive.current || started !== accessVersion.current) return;
    if (pw.current) pw.current.value = '';
    if (discard || value.current.id !== id || !value.current.dirty) {
      setBaseVersion(t.version);
      setReply('');
    }
    setConfirmClose(false);
    setCurrent(t);
  }
  useEffect(() => {
    alive.current = true;
    try {
      setPending(readOriginal(actor));
    } catch (e) {
      setBlocked(true);
      problem(e);
    }
    void list().catch(problem);
    function verify() {
      if (actor.staff && !document.hidden && !locked.current) void list().catch(problem);
    }
    window.addEventListener('focus', verify);
    return () => {
      alive.current = false;
      ++accessVersion.current;
      ++counter.current;
      window.removeEventListener('focus', verify);
    };
  }, []);
  async function action(fn: () => Promise<void>) {
    if (locked.current) return;
    locked.current = true;
    setBusy(true);
    setError('');
    try {
      await fn();
    } catch (e) {
      problem(e);
    } finally {
      locked.current = false;
      if (alive.current) setBusy(false);
    }
  }
  async function refresh() {
    await list();
    if (alive.current && current) await detail(current.id);
  }
  async function accepted(t: Ticket, i: Original, started: number) {
    if (!alive.current || started !== accessVersion.current) return;
    checkedReceipt(t, actor, i);
    forgetOriginal(actor, i);
    setPending(readOriginal(actor));
    setLast({ op: i.op, version: t.version });
    setConfirmClose(false);
    setReply('');
    if (i.op === 'create') {
      if (composer.current) composer.current.open = false;
      setDescription('');
      setSubject('');
    }
    await detail(t.id, true);
    if (alive.current) await list();
  }
  async function mutate(op: Op, password = '') {
    const started = accessVersion.current;
    if (pending || blocked || !ready || actor.staff !== (op === 'reply')) return;
    if (op === 'create' && (reply || !validText(subject, true) || !validText(description))) return;
    if (
      op !== 'create' &&
      (!current ||
        current.state !== 'OPEN' ||
        (op !== 'close' && (!validText(reply) || baseVersion !== current.version || current.version >= 31)))
    )
      return;
    if (
      op === 'reply' &&
      (!password || password.includes('\0') || new TextEncoder().encode(password).length > 72)
    )
      throw Error(uiText('请输入有效的当前密码（最多 72 个 UTF-8 字节）。'));
    const i: Original = {
      op,
      key: crypto.randomUUID(),
      expected: op === 'create' ? 0 : current!.version,
      ...(op === 'create' ? {} : { id: current!.id }),
    };
    keepOriginal(actor, i);
    setPending(i);
    setLast(null);
    let t: Ticket;
    try {
      t = await supportApi.submit(actor, i, {
        category,
        subject,
        body: op === 'create' ? description : reply,
        password,
      });
    } catch (e) {
      if (e instanceof ApiError && !e.uncertain) {
        forgetOriginal(actor, i);
        if (alive.current) {
          setPending(readOriginal(actor));
          setConfirmClose(false);
          if (e.code === 'SUPPORT_TICKET_CHANGED' && i.id) {
            await detail(i.id);
            await list();
          }
        }
        throw e;
      }
      throw Error(uiText('提交回执尚未确认。请先使用原操作编号只读核对。'));
    }
    await accepted(t, i, started);
  }
  async function recover(i: Original) {
    const started = accessVersion.current;
    let t: Ticket;
    try {
      t = await supportApi.receipt(actor, i);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        if (actor.staff) clearPrivate();
        throw Error(uiText('暂未查到原收据，或当前已无权读取。请核对权限后再次查询。'));
      }
      throw e;
    }
    await accepted(t, i, started);
  }
  const noWrite = busy || !!pending || blocked || !ready;
  const conflict = !!reply && !!current && baseVersion !== current.version;
  function sendReply() {
    const password = pw.current?.value || '';
    if (pw.current) pw.current.value = '';
    void action(() => mutate(actor.staff ? 'reply' : 'followup', password));
  }
  return (
    <div className="support-desk">
      {error && (
        <div className="support-notice" role="alert">
          {error}
        </div>
      )}
      {pending && (
        <div className="support-notice" role="status">
          <strong>{operations[pending.op]}{uiText("待核对")}</strong>
          <p>{uiText("核对原操作收据后可继续处理。")}</p>
          <button disabled={busy} onClick={() => void action(() => recover(pending))}>{uiText("只读核对原工单收据")}</button>
        </div>
      )}
      {last && (
        <p className="support-receipt" role="status" data-support-receipt={last.op}>{uiText("已核对")}{operations[last.op]}{uiText("原收据 · 操作当时第")} {last.version} {uiText("版")}</p>
      )}
      {!actor.staff && (
        <details className="support-compose" ref={composer}>
          <summary>{uiText("新建客服工单")}</summary>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void action(() => mutate('create'));
            }}
          >
            <label>{uiText("问题分类")}<select
                value={category}
                disabled={noWrite}
                onChange={(e) => setCategory(e.target.value as Category)}
              >
                {Object.entries(categories).map(([k, n]) => (
                  <option key={k} value={k}>
                    {n}
                  </option>
                ))}
              </select>
            </label>
            <label>{uiText("工单主题")}<input
                value={subject}
                aria-label={uiText("工单主题")}
                maxLength={240}
                onChange={(e) => setSubject(e.target.value)}
                disabled={noWrite}
                required
              />
            </label>
            <label>{uiText("问题说明")}<textarea
                value={description}
                aria-label={uiText("问题说明")}
                maxLength={4000}
                rows={3}
                onChange={(e) => setDescription(e.target.value)}
                disabled={noWrite}
                required
              />
            </label>
            <small>{uiText("主题最多 120 字。说明最多 2000 字、6000 字节，请勿填写账户密码或密钥。")}</small>
            <button
              className="support-primary"
              type="submit"
              disabled={noWrite || !!reply || !validText(subject, true) || !validText(description)}
            >{uiText("提交工单")}</button>
          </form>
        </details>
      )}
      <div className="support-columns">
        <section className="support-index" aria-label={uiText("工单列表")}>
          <div className="support-row">
            <h2>{actor.staff ? uiText('工作区工单') : uiText('本人记录')}</h2>
            <button disabled={busy} onClick={() => void action(refresh)}>{uiText("刷新工单")}</button>
          </div>
          {rows.map((c) => (
            <button
              className="support-list-item"
              key={c.id}
              data-support-item={c.id}
              aria-pressed={current?.id === c.id}
              disabled={busy || (!!reply && !!current && current.id !== c.id)}
              onClick={() => void action(() => detail(c.id))}
            >
              <strong>{c.subject}</strong>
              <small>
                {categories[c.category]} · {stateTitle(c.state)} {uiText("· 第")} {c.version} {uiText("版")}</small>
            </button>
          ))}
          {!rows.length && <p>{ready ? uiText('暂无工单。') : uiText('正在核对工单列表…')}</p>}
          {cursor && (
            <button disabled={busy} onClick={() => void action(() => list(cursor))}>{uiText("加载更早工单")}</button>
          )}
        </section>
        <section className="support-detail" aria-label={uiText("工单详情")}>
          {!current ? (
            <p>{uiText("从列表打开工单，查看完整对话。")}</p>
          ) : (
            <article
              data-support-ticket={current.id}
              data-support-version={current.version}
              data-support-state={current.state}
            >
              <div className="support-row">
                <h2>{current.subject}</h2>
                <span>
                  {stateTitle(current.state)} {uiText("· 第")} {current.version} {uiText("版")}</span>
              </div>
              <p>
                <code>{current.id}</code>
              </p>
              <ol className="support-thread">
                {current.messages.map((m) => (
                  <li key={m.version} data-message-kind={m.kind}>
                    <div>
                      <strong>{kinds[m.kind]}</strong>
                      <time dateTime={m.created_at}>{formatTime(m.created_at)}</time>
                    </div>
                    {m.body && <p>{m.body}</p>}
                  </li>
                ))}
              </ol>
              {current.state === 'OPEN' && current.version < 31 && (
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    sendReply();
                  }}
                >
                  <label>
                    {actor.staff ? uiText('给用户的回复') : uiText('补充问题')}
                    <textarea
                      rows={3}
                      aria-label={actor.staff ? uiText('客服答复') : uiText('用户追问')}
                      maxLength={4000}
                      value={reply}
                      disabled={noWrite}
                      onChange={(e) => {
                        if (!reply) setBaseVersion(current.version);
                        setReply(e.target.value);
                      }}
                    />
                  </label>
                  {reply && !validText(reply) && (
                    <small role="status">{uiText("内容须为有效文字，最多 2000 字及 6000 字节。")}</small>
                  )}
                  {conflict && (
                    <div className="support-notice">
                      <p>{uiText("已保留本页文字。请先阅读最新的第")} {current.version} {uiText("版。")}</p>
                      <button
                        type="button"
                        disabled={noWrite}
                        onClick={() => setBaseVersion(current.version)}
                      >{uiText("基于当前版本继续编辑")}</button>
                    </div>
                  )}
                  {actor.staff && (
                    <label>{uiText("客服当前密码")}<input
                        type="password"
                        aria-label={uiText("客服当前密码")}
                        autoComplete="off"
                        ref={pw}
                        maxLength={72}
                        required
                        disabled={noWrite || conflict}
                      />
                    </label>
                  )}
                  <div className="support-actions">
                    <button
                      className="support-primary"
                      type="submit"
                      disabled={noWrite || conflict || !validText(reply)}
                    >
                      {actor.staff ? uiText('确认发送客服答复') : uiText('提交追问')}
                    </button>
                    {!!reply && (
                      <button
                        type="button"
                        disabled={noWrite}
                        onClick={() => {
                          setReply('');
                          setBaseVersion(current.version);
                          if (pw.current) pw.current.value = '';
                        }}
                      >{uiText("清空本页输入")}</button>
                    )}
                  </div>
                </form>
              )}
              {current.state === 'OPEN' && current.version >= 31 && <p>{uiText("已达消息上限，用户可关闭此工单。")}</p>}
              {!actor.staff &&
                current.state === 'OPEN' &&
                (confirmClose ? (
                  <div className="support-notice">
                    <p>{uiText("确认关闭后，本工单将结束，已有记录仍可查看。")}</p>
                    <div className="support-actions">
                      <button disabled={noWrite} onClick={() => void action(() => mutate('close'))}>{uiText("确认关闭工单")}</button>
                      <button disabled={busy} onClick={() => setConfirmClose(false)}>{uiText("返回")}</button>
                    </div>
                  </div>
                ) : (
                  <button disabled={noWrite || !!reply} onClick={() => setConfirmClose(true)}>{uiText("关闭工单")}</button>
                ))}
              {(current.state === 'CLOSED' || current.version >= 31) && reply && (
                <div className="support-notice">
                  <label>{uiText("本页尚未发送的文字")}<textarea value={reply} readOnly />
                  </label>
                  <button onClick={() => setReply('')}>{uiText("清空本页输入")}</button>
                </div>
              )}
              {current.state === 'CLOSED' && <p>{uiText("工单已关闭，以上为本次服务记录。")}</p>}
            </article>
          )}
        </section>
      </div>
    </div>
  );
}
