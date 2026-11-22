import { uiText, currentLocale } from './locale';
import { useEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import {
  checkReceipt,
  collabApi,
  opLabel,
  readTicket,
  relationSlot,
  roleLabel,
  stateLabel,
  validMember,
} from './collaboration';
import type { Member, Operation, Receipt, RelationTicket, Role } from './collaboration';
import { SharedDraftEditor } from './SharedDraftEditor';
import type { DraftAccess } from './SharedDraftEditor';
import './collaboration.css';
type Props = {
  user: string;
  project: string;
  projectName: string;
  enabled: boolean;
  visible: boolean;
  onDirty: (dirty: boolean) => void;
  onExpired: () => void;
};
type Decision = {
  op: Operation;
  project: string;
  target: string;
  version: number;
  id?: string;
  role: Role;
  name: string;
};
export function ProjectCollaboration({
  user,
  project,
  projectName,
  enabled,
  visible,
  onDirty,
  onExpired,
}: Props) {
  const [members, setMembers] = useState<Member[]>([]),
    [incoming, setIncoming] = useState<Member[]>([]),
    [shared, setShared] = useState<Member[]>([]),
    [manage, setManage] = useState(false);
  const [verified, setVerified] = useState(false);
  const [target, setTarget] = useState(''),
    [role, setRole] = useState<Role>('viewer'),
    [decision, setDecision] = useState<Decision | null>(null),
    [pending, setPending] = useState<RelationTicket | null>(null);
  const [receipt, setReceipt] = useState<Receipt | null>(null),
    [selected, setSelected] = useState<DraftAccess | null>(null),
    [next, setNext] = useState<DraftAccess | null | undefined>(undefined),
    [draftDirty, setDraftDirty] = useState(false);
  const [busy, setBusy] = useState(false),
    [blocked, setBlocked] = useState(false),
    [notice, setNotice] = useState(''),
    [error, setError] = useState('');
  const seq = useRef(0),
    alive = useRef(true),
    writing = useRef(false),
    selectedRef = useRef<DraftAccess | null>(null),
    readBusy = useRef(false);
  const slot = relationSlot(user);
  selectedRef.current = selected;
  useEffect(() => {
    alive.current = true;
    try {
      setPending(readTicket<RelationTicket>(slot));
    } catch {
      setBlocked(true);
      setError(uiText('原成员操作编号无法读取。'));
    }
    return () => {
      alive.current = false;
      seq.current++;
    };
  }, [slot]);
  useEffect(() => {
    onDirty(draftDirty || !!pending || !!decision);
    return () => onDirty(false);
  }, [draftDirty, decision, pending, onDirty]);
  function err(e: unknown) {
    if (!alive.current) return;
    if (e instanceof ApiError && e.status === 401) {
      onExpired();
      return;
    }
    setError(e instanceof Error ? e.message : uiText('项目协作请求未完成。'));
  }
  function closeShared(message: string) {
    setSelected(null);
    setNext(undefined);
    setDraftDirty(false);
    setNotice(message);
  }
  async function sync(force = false) {
    if (readBusy.current && !force) return;
    readBusy.current = true;
    const n = ++seq.current;
    try {
      const [a, b, c] = await Promise.allSettled([
        collabApi.members(project),
        collabApi.invitations(),
        collabApi.projects(),
      ]);
      if (!alive.current || n !== seq.current) return;
      if (
        c.status === 'fulfilled' &&
        Array.isArray(c.value.items) &&
        c.value.items.every((m) => validMember(m) && m.target_user_id === user && m.state === 'ACTIVE')
      ) {
        setShared(c.value.items);
        setVerified(true);
        const opened = selectedRef.current;
        if (opened && opened.role !== 'owner') {
          const access = c.value.items.find(
            (m) => m.project_id === opened.id && m.invitation_id === opened.round,
          );
          if (!access) closeShared(uiText('共享权限已失效，已退出该项目草稿。'));
          else if (access.role !== opened.role) {
            setSelected({ ...opened, role: access.role });
            setDraftDirty(false);
            setNotice(uiText('成员权限已更新为') + roleLabel(access.role) + '。');
          }
        }
      } else {
        setShared([]);
        setVerified(false);
        if (selectedRef.current?.role !== 'owner' && selectedRef.current)
          closeShared(uiText('无法核对当前共享权限，已关闭共享草稿。'));
        err(c.status === 'rejected' ? c.reason : Error(uiText('共享项目范围不符。')));
      }
      if (
        b.status === 'fulfilled' &&
        Array.isArray(b.value.items) &&
        b.value.items.every((m) => validMember(m) && m.target_user_id === user)
      )
        setIncoming(b.value.items);
      else {
        setIncoming([]);
        err(b.status === 'rejected' ? b.reason : Error(uiText('邀请收件人不符。')));
      }
      if (
        a.status === 'fulfilled' &&
        Array.isArray(a.value.items) &&
        a.value.items.every((m) => validMember(m) && m.project_id === project)
      ) {
        setManage(true);
        setMembers(a.value.items);
      } else {
        setManage(false);
        setMembers([]);
        if (selectedRef.current?.role === 'owner') closeShared(uiText('当前项目管理权限已变化。'));
        if (
          a.status === 'rejected' &&
          !(a.reason instanceof ApiError && [403, 404].includes(a.reason.status))
        )
          err(a.reason);
      }
    } finally {
      if (seq.current === n) readBusy.current = false;
    }
  }
  useEffect(() => {
    if (!enabled || !visible) {
      setVerified(false);
      return;
    }
    void sync(true);
    const refresh = () => {
      if (!document.hidden) void sync();
    };
    const timer = window.setInterval(refresh, 7000);
    window.addEventListener('focus', refresh);
    document.addEventListener('visibilitychange', refresh);
    return () => {
      ++seq.current;
      readBusy.current = false;
      window.clearInterval(timer);
      window.removeEventListener('focus', refresh);
      document.removeEventListener('visibilitychange', refresh);
    };
  }, [enabled, visible, user, project]);
  function choose(a: DraftAccess | null) {
    if (selected?.id === a?.id && selected?.round === a?.round) return;
    if (draftDirty) setNext(a);
    else {
      setSelected(a);
      setNext(undefined);
    }
  }
  function prepare(op: Operation, m: Member) {
    setError('');
    setDecision({
      op,
      project: m.project_id,
      target: m.target_user_id,
      version: m.version,
      id: m.invitation_id,
      role: m.role,
      name: m.project_name,
    });
  }
  async function invite() {
    if (writing.current || pending || blocked) return;
    const id = target.trim();
    if (!/^usr_[0-9a-f]{32}$/.test(id) || id === user) {
      setError(uiText('请填写对方在协作页显示的账户编号。'));
      return;
    }
    writing.current = true;
    setBusy(true);
    setError('');
    setDecision(null);
    try {
      const { items } = await collabApi.members(project, id);
      if (!alive.current) return;
      if (
        !Array.isArray(items) ||
        items.length > 1 ||
        items.some((m) => !validMember(m) || m.project_id !== project || m.target_user_id !== id)
      )
        throw Error(uiText('目标成员核对范围不符。'));
      const prior = items[0];
      if (prior && ['PENDING', 'ACTIVE'].includes(prior.state))
        throw Error(uiText('当前成员已有有效邀请或权限，请查看成员列表。'));
      setDecision({
        op: 'invite',
        project,
        target: id,
        version: prior?.version || 0,
        role,
        name: projectName,
      });
    } catch (e) {
      err(e);
    } finally {
      writing.current = false;
      if (alive.current) setBusy(false);
    }
  }
  function clear(t: RelationTicket) {
    if (readTicket<RelationTicket>(slot)?.key === t.key) sessionStorage.removeItem(slot);
    if (alive.current) setPending(readTicket<RelationTicket>(slot));
  }
  async function accept(result: Receipt, t: RelationTicket) {
    checkReceipt(result, t);
    clear(t);
    if (alive.current) {
      setReceipt(result);
      setDecision(null);
      setNotice(uiText('已核对「') + opLabel(t.operation) + uiText('」的原操作收据。'));
      await sync(true);
    }
  }
  async function mutate() {
    const d = decision;
    if (!d || writing.current || pending || blocked) return;
    writing.current = true;
    setBusy(true);
    setError('');
    setNotice('');
    const t: RelationTicket = {
      operation: d.op,
      project: d.project,
      target: d.target,
      expected: d.version,
      id: d.op === 'invite' ? undefined : d.id,
      key: crypto.randomUUID(),
    };
    try {
      sessionStorage.setItem(slot, JSON.stringify(t));
      setPending(t);
      await accept(await collabApi.mutate(t, d.role), t);
    } catch (e) {
      if (e instanceof ApiError && !e.uncertain) {
        clear(t);
        if (alive.current) {
          setDecision(null);
          void sync(true);
        }
      }
      err(e);
    } finally {
      writing.current = false;
      if (alive.current) setBusy(false);
    }
  }
  async function recover() {
    const t = pending;
    if (!t || writing.current) return;
    writing.current = true;
    setBusy(true);
    setError('');
    try {
      await accept(await collabApi.receipt(t), t);
    } catch (e) {
      err(e);
    } finally {
      writing.current = false;
      if (alive.current) setBusy(false);
    }
  }
  const locked = blocked || busy || !!pending;
  if (!enabled)
    return (
      <section className="co-work">
        <h1>{uiText("项目协作")}</h1>
        <p>{uiText("站内邀请与共享节点草稿需要 PostgreSQL 和账户登录。")}</p>
      </section>
    );
  return (
    <section className="co-work">
      <header className="co-line">
        <div>
          <h1>{uiText("项目协作")}</h1>
          <p>{uiText("邀请已有账户查看或编辑项目节点草稿。")}</p>
        </div>
        <button
          onClick={() => {
            setError('');
            void sync(true);
          }}
        >{uiText("刷新协作权限")}</button>
      </header>
      <div className="co-identity">
        <span>{uiText("我的账户编号")}</span>
        <code data-collab-user={user}>{user}</code>
        <small>{uiText("可将编号提供给项目所有者。")}</small>
      </div>
      {error && (
        <p role="alert" className="co-warn">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="co-status">
          {notice}
        </p>
      )}
      {pending && !busy && (
        <div className="co-warn">
          「{opLabel(pending.operation)}{uiText("」回执待核对。")}<button onClick={recover}>{uiText("只读核对原成员收据")}</button>
        </div>
      )}
      {receipt && (
        <details className="co-receipt">
          <summary>{uiText("查看原成员操作收据")}</summary>
          <p>
            {opLabel(receipt.operation)} · {receipt.project_name} {uiText("· 第")} {receipt.round} {uiText("轮 · r")}{receipt.version}{' '}{uiText("· 当时")}{stateLabel(receipt.state)}
          </p>
          <code>{receipt.target_user_id}</code>
        </details>
      )}
      <div className="co-grid">
        <section className="co-panel">
          <h2>{uiText("收到的站内邀请")}</h2>
          {incoming
            .filter((m) => m.state === 'PENDING')
            .map((m) => (
              <div className="co-member" key={m.invitation_id}>
                <div>
                  <strong>{m.project_name}</strong>
                  <small>
                    {m.inviter_name} {uiText("邀请你 ·")} {roleLabel(m.role)} {uiText("· 第")} {m.round} {uiText("轮")}{m.expires_at ? uiText(' · 截止 ') + new Date(m.expires_at).toLocaleDateString(currentLocale()) : ''}
                  </small>
                </div>
                <button disabled={locked} onClick={() => prepare('accept', m)}>{uiText("接受邀请")}</button>
              </div>
            ))}
          {!incoming.some((m) => m.state === 'PENDING') && <p>{uiText("暂无待接受的邀请。")}</p>}
        </section>
        <section className="co-panel">
          <h2>{uiText("我参与的项目")}</h2>
          {shared.map((m) => (
            <div className="co-member" key={m.invitation_id}>
              <div>
                <strong>{m.project_name}</strong>
                <small>
                  {roleLabel(m.role)} {uiText("· 第")} {m.round} {uiText("轮")}</small>
              </div>
              <button
                data-shared-project={m.project_id}
                onClick={() =>
                  choose({ id: m.project_id, name: m.project_name, role: m.role, round: m.invitation_id })
                }
              >{uiText("打开共享草稿")}</button>
            </div>
          ))}
          {!shared.length && <p>{uiText("接受站内邀请后可在此打开草稿。")}</p>}
        </section>
      </div>
      {manage && (
        <details className="co-owner">
          <summary>{uiText("管理我的项目 ·")} {projectName}</summary>
          <div className="co-line">
            <h2>{uiText("当前项目成员")}</h2>
            <button onClick={() => choose({ id: project, name: projectName, role: 'owner', round: 'owner' })}>{uiText("打开本项目草稿")}</button>
          </div>
          <div className="co-invite">
            <label>{uiText("对方账户编号")}<input
                aria-label={uiText("对方账户编号")}
                value={target}
                disabled={locked}
                onChange={(ev) => setTarget(ev.target.value.trim())}
                placeholder="usr_…"
              />
            </label>
            <label>{uiText("邀请权限")}<select
                aria-label={uiText("邀请权限")}
                disabled={locked}
                value={role}
                onChange={(ev) => setRole(ev.target.value as Role)}
              >
                <option value="viewer">{uiText("只读")}</option>
                <option value="editor">{uiText("可编辑")}</option>
              </select>
            </label>
            <button className="co-primary" disabled={locked || !target} onClick={invite}>{uiText("邀请协作者")}</button>
          </div>
          {members.map((m) => (
            <div className="co-member" data-member-id={m.target_user_id} key={m.target_user_id}>
              <div>
                <code>{m.target_user_id}</code>
                <small>
                  {stateLabel(m.state)} · {roleLabel(m.role)} {uiText("· 第")} {m.round} {uiText("轮 · r")}{m.version}
                </small>
              </div>
              <div>
                {m.state === 'PENDING' && (
                  <button disabled={locked} onClick={() => prepare('withdraw', m)}>{uiText("撤回邀请")}</button>
                )}
                {m.state === 'ACTIVE' && m.role === 'editor' && (
                  <button disabled={locked} onClick={() => prepare('downgrade', m)}>{uiText("降为只读")}</button>
                )}
                {m.state === 'ACTIVE' && (
                  <button disabled={locked} onClick={() => prepare('revoke', m)}>{uiText("收回权限")}</button>
                )}
              </div>
            </div>
          ))}
          {!members.length && <p>{uiText("当前项目尚无站内成员。")}</p>}
        </details>
      )}
      {decision && !pending && (
        <section className="co-confirm">
          <h2>
            {opLabel(decision.op)} · {decision.name}
          </h2>
          <p>{uiText("目标账户")} <code>{decision.target}</code>
          </p>
          <p>
            {decision.op === 'invite' || decision.op === 'accept'
              ? uiText('草稿权限为') + roleLabel(decision.role) + '。'
              : decision.op === 'downgrade'
                ? uiText('所有仍有效会话将只可读取本项目草稿。')
                : decision.op === 'withdraw'
                  ? uiText('本轮邀请将失效。')
                  : uiText('目标账户在本项目的草稿访问将终止。')}
          </p>
          <button onClick={() => setDecision(null)}>{uiText("返回核对")}</button>
          <button className="co-primary" disabled={locked} onClick={() => void mutate()}>{uiText("确认")}{opLabel(decision.op)}
          </button>
        </section>
      )}
      {next !== undefined && (
        <div className="co-warn">{uiText("切换将结束当前共享草稿的本页编辑。")}<button onClick={() => setNext(undefined)}>{uiText("留在当前草稿")}</button>
          <button
            onClick={() => {
              setSelected(next);
              setNext(undefined);
              setDraftDirty(false);
            }}
          >{uiText("结束本页编辑并继续")}</button>
        </div>
      )}
      {selected && (
        <div hidden={selected.role !== 'owner' && !verified}>
          <div className="co-line">
            <span>{uiText("个人活动项目仍为「")}{projectName}」</span>
            <button onClick={() => choose(null)}>{uiText("退出当前草稿")}</button>
          </div>
          <SharedDraftEditor
            key={JSON.stringify([user, selected.id, selected.role, selected.round])}
            user={user}
            access={selected}
            visible={visible && (selected.role === 'owner' || verified)}
            onDirty={setDraftDirty}
            onExpired={onExpired}
            onDenied={() => {
              closeShared(uiText('共享权限已变化，已关闭原草稿。'));
              void sync(true);
            }}
          />
        </div>
      )}
    </section>
  );
}
