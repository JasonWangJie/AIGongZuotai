import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import { copyNodes, draftNodesValid, sameNodes } from './draftState';
import type { CloudDraft, DraftNode } from './types';
import { checkDraft, collabApi, draftSlot, readTicket, roleLabel } from './collaboration';
import type { DraftTicket, Role } from './collaboration';
export type DraftAccess = { id: string; name: string; role: Role | 'owner'; round: string };
type Props = {
  user: string;
  access: DraftAccess;
  visible: boolean;
  onDirty: (v: boolean) => void;
  onDenied: () => void;
  onExpired: () => void;
};
export function SharedDraftEditor({ user, access, visible, onDirty, onDenied, onExpired }: Props) {
  const p = access.id,
    canEdit = access.role !== 'viewer',
    slot = draftSlot(user, p, access.round);
  const [base, setBase] = useState<CloudDraft | null>(null),
    [nodes, setNodes] = useState<DraftNode[]>([]),
    [pending, setPending] = useState<DraftTicket | null>(null);
  const [history, setHistory] = useState<{ revision: number; node_count: number }[]>([]),
    [before, setBefore] = useState(0),
    [snapshot, setSnapshot] = useState<CloudDraft | null>(null);
  const [conflict, setConflict] = useState<CloudDraft | null>(null),
    [busy, setBusy] = useState(''),
    [error, setError] = useState(''),
    [notice, setNotice] = useState(''),
    [blocked, setBlocked] = useState(false);
  const alive = useRef(true),
    lock = useRef(false);
  const dirty = !!base && !sameNodes(base.nodes, nodes),
    guarded = dirty || !!pending,
    locked = !!busy || !!pending || blocked || !base;
  function report(e: unknown) {
    if (!alive.current) return;
    if (e instanceof ApiError) {
      if (e.status === 401) {
        onExpired();
        return;
      }
      if (e.code === 'COLLABORATION_NOT_FOUND' || e.code === 'COLLABORATION_READ_ONLY') {
        setBase(null);
        setNodes([]);
        setHistory([]);
        setSnapshot(null);
        setConflict(null);
        onDenied();
        return;
      }
    }
    setError(e instanceof Error ? e.message : uiText('协作草稿请求失败。'));
  }
  function install(d: CloudDraft) {
    setBase(d);
    setNodes(copyNodes(d.nodes));
    setConflict(null);
    setSnapshot(null);
  }
  function clear(t: DraftTicket) {
    if (readTicket<DraftTicket>(slot)?.key === t.key) sessionStorage.removeItem(slot);
    if (alive.current) setPending(readTicket<DraftTicket>(slot));
  }
  async function run(label: string, fn: () => Promise<void>) {
    if (lock.current) return;
    lock.current = true;
    setBusy(label);
    setError('');
    setNotice('');
    try {
      await fn();
    } catch (e) {
      report(e);
    } finally {
      lock.current = false;
      if (alive.current) setBusy('');
    }
  }
  async function revisions(more = false) {
    const page = await collabApi.history(p, more ? before : undefined);
    if (
      !Array.isArray(page.items) ||
      page.items.some((r) => !Number.isSafeInteger(r.revision) || r.revision < 1)
    )
      throw Error(uiText('草稿历史版本无效。'));
    if (alive.current) {
      setHistory((old) => [
        ...new Map([...(more ? old : []), ...page.items].map((r) => [r.revision, r])).values(),
      ]);
      setBefore(page.next_before_revision || 0);
    }
  }
  async function latest(preserve: boolean) {
    const fresh = checkDraft(await collabApi.draft(p), p);
    if (!alive.current) return;
    if (!preserve) install(fresh);
    else if (base && fresh.revision > base.revision) setConflict(fresh);
    await revisions();
  }
  useEffect(() => {
    alive.current = true;
    try {
      setPending(readTicket<DraftTicket>(slot));
    } catch {
      setBlocked(true);
      setError(uiText('原草稿保存编号无法读取。'));
    }
    void run(uiText('读取草稿'), () => latest(false));
    return () => {
      alive.current = false;
    };
  }, [slot]);
  useEffect(() => {
    onDirty(guarded);
    return () => onDirty(false);
  }, [guarded, onDirty]);
  useEffect(() => {
    if (!guarded) return;
    const guard = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  }, [guarded]);
  const observed = useRef({ dirty, pending, base });
  observed.current = { dirty, pending, base };
  useEffect(() => {
    if (!visible) return;
    const focus = () => {
      if (!lock.current && alive.current && !observed.current.dirty && !observed.current.pending)
        void run(uiText('读取草稿'), () => latest(false));
    };
    window.addEventListener('focus', focus);
    return () => window.removeEventListener('focus', focus);
  }, [visible, slot]);
  function change(i: number, data: Partial<DraftNode>) {
    setNodes((old) => old.map((n, k) => (k === i ? { ...n, ...data } : n)));
    setNotice('');
  }
  function accept(result: CloudDraft, t: DraftTicket) {
    const saved = checkDraft(result, p, t.expected + 1);
    clear(t);
    if (alive.current) {
      install(saved);
      setNotice(uiText('已确认本人保存 r') + saved.revision);
    }
  }
  function save() {
    if (locked || !canEdit || !base || !dirty) return;
    if (!draftNodesValid(nodes)) {
      setError(uiText('节点需有独立编号，标题最多 80 字、正文最多 5000 字，每稿最多 40 个节点。'));
      return;
    }
    const t = { expected: base.revision, key: crypto.randomUUID() };
    void run(uiText('保存草稿'), async () => {
      sessionStorage.setItem(slot, JSON.stringify(t));
      setPending(t);
      try {
        const d = await collabApi.save(p, { expected_revision: t.expected, nodes: copyNodes(nodes) }, t.key);
        accept(d, t);
        await revisions();
      } catch (e) {
        if (e instanceof ApiError && !e.uncertain) {
          clear(t);
          if (e.code === 'DRAFT_CONFLICT') {
            const d = checkDraft(await collabApi.draft(p), p);
            if (alive.current) setConflict(d);
            return;
          }
        }
        throw e;
      }
    });
  }
  function recover() {
    if (!pending) return;
    const t = pending;
    void run(uiText('核对原草稿收据'), async () => {
      const saved = await collabApi.saved(p, t.key);
      accept(saved, t);
      await revisions();
    });
  }
  function historical(n: number) {
    void run(uiText('读取历史'), async () => {
      const d = checkDraft(await collabApi.revision(p, n), p, n);
      if (alive.current) setSnapshot(d);
    });
  }
  return (
    <section className="co-editor" aria-label={uiText("协作草稿编辑器")}>
      <header className="co-line">
        <div>
          <h2>{access.name} {uiText("· 节点草稿")}</h2>
          <small>
            {roleLabel(access.role)}
            {base ? uiText(' · 本页基于 r') + base.revision : ''}
          </small>
        </div>
        <button disabled={!!busy} onClick={() => void run(uiText('刷新草稿'), () => latest(dirty || !!pending))}>{uiText("刷新草稿")}</button>
      </header>
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
        <div className="co-warn">{uiText("本次保存尚待核对。")}<button onClick={recover}>{uiText("只读核对原草稿收据")}</button>
        </div>
      )}
      {conflict && (
        <div className="co-warn">{uiText("服务器为 r")}{conflict.revision}{uiText("，本页输入仍保留。")}<details>
            <summary>{uiText("查看服务器节点")}</summary>
            {conflict.nodes.map((n) => (
              <div key={n.id}>
                <strong>{n.title}</strong>
                <p>{n.note}</p>
              </div>
            ))}
          </details>
          <button disabled={!!busy || !!pending} onClick={() => install(conflict)}>{uiText("采用服务器草稿")}</button>
          {canEdit && (
            <button
              disabled={!!busy || !!pending}
              onClick={() => {
                setBase(conflict);
                setConflict(null);
                setNotice(uiText('已保留本页节点。再次保存会创建新修订。'));
              }}
            >{uiText("保留本页并基于最新修订")}</button>
          )}
        </div>
      )}
      {base && (
        <>
          <ol className="co-nodes">
            {nodes.map((n, i) => (
              <li key={n.id}>
                <b>{String(i + 1).padStart(2, '0')}</b>
                {canEdit ? (
                  <div>
                    <label>{uiText("节点标题")}<input
                        aria-label={uiText('节点标题 ') + (i + 1)}
                        disabled={locked}
                        value={n.title}
                        onChange={(ev) => change(i, { title: ev.target.value })}
                      />
                    </label>
                    <label>{uiText("节点正文")}<textarea
                        aria-label={uiText('节点正文 ') + (i + 1)}
                        rows={3}
                        disabled={locked}
                        value={n.note}
                        onChange={(ev) => change(i, { note: ev.target.value })}
                      />
                    </label>
                    <button
                      disabled={locked}
                      onClick={() => setNodes((old) => old.filter((_, k) => i !== k))}
                    >{uiText("移除节点")} {i + 1}
                    </button>
                  </div>
                ) : (
                  <div>
                    <strong>{n.title || uiText('未命名节点')}</strong>
                    <p>{n.note}</p>
                  </div>
                )}
              </li>
            ))}
          </ol>
          {!nodes.length && <p>{uiText("这份草稿暂无节点。")}</p>}
          {canEdit && (
            <div className="co-buttons">
              <button
                disabled={locked || nodes.length >= 40}
                onClick={() => setNodes((a) => [...a, { id: crypto.randomUUID(), title: '', note: '' }])}
              >{uiText("添加共享节点")}</button>
              <button className="co-primary" disabled={locked || !dirty || !!conflict} onClick={save}>{uiText("保存共享草稿")}</button>
            </div>
          )}
          <details className="co-history">
            <summary>{uiText("草稿修订历史")}</summary>
            {history.map((h) => (
              <button key={h.revision} disabled={!!busy} onClick={() => historical(h.revision)}>{uiText("读取 r")}{h.revision} · {h.node_count} {uiText("个节点")}</button>
            ))}
            {before > 0 && (
              <button disabled={!!busy} onClick={() => void run(uiText('读取历史'), () => revisions(true))}>{uiText("更多修订")}</button>
            )}
          </details>
        </>
      )}
      {snapshot && (
        <div className="co-history-view">
          <h3>{uiText("草稿 r")}{snapshot.revision}</h3>
          {snapshot.nodes.map((n) => (
            <div key={n.id}>
              <strong>{n.title}</strong>
              <p>{n.note}</p>
            </div>
          ))}
          <button onClick={() => setSnapshot(null)}>{uiText("收起历史内容")}</button>
        </div>
      )}
    </section>
  );
}
