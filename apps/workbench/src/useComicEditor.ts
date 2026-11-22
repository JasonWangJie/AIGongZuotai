import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import { blankComic, comicApi, comicProblem, comicSlot, matchesComic, readComicTicket } from './comics';
import type { Comic, ComicSummary, ComicTicket, ComicVersion } from './comics';
import type { Bootstrap } from './types';
import { comicPowerPoint } from './comicExport';
import { downloadBlob } from './presentationExport';
export type ComicProps = {
  bootstrap?: Bootstrap;
  owner: string;
  project: string;
  visible: boolean;
  enabled: boolean;
  onDirty: (dirty: boolean) => void;
  onExpired: () => void;
};
export function useComicEditor({ owner, project, visible, enabled, onDirty, onExpired }: ComicProps) {
  const [doc, setDoc] = useState<Comic>(blankComic),
    [base, setBase] = useState<ComicVersion | null>(null),
    [dirty, setDirty] = useState(false),
    [ready, setReady] = useState(false);
  const [rows, setRows] = useState<ComicSummary[]>([]),
    [before, setBefore] = useState(0),
    [busy, setBusy] = useState(''),
    [error, setError] = useState(''),
    [status, setStatus] = useState('');
  const [pending, setPending] = useState<ComicTicket | null>(null),
    [blocked, setBlocked] = useState(false),
    [conflict, setConflict] = useState(false);
  const [choice, setChoice] = useState(0),
    [confirming, setConfirming] = useState(false),
    [merge, setMerge] = useState<ComicVersion | null>(null);
  const alive = useRef(true),
    lock = useRef(false),
    initial = useRef(false),
    listSeq = useRef(0),
    abort = useRef<AbortController | null>(null);
  const slot = comicSlot(owner, project),
    guarded = dirty || !!pending,
    latest = Math.max(base?.version || 0, ...rows.map((r) => r.version));
  useEffect(() => {
    alive.current = true;
    try {
      setPending(readComicTicket(slot));
    } catch {
      setBlocked(true);
      setError(uiText('原操作记录无法读取。'));
    }
    return () => {
      alive.current = false;
      abort.current?.abort();
    };
  }, [slot]);
  useEffect(() => {
    onDirty(guarded);
    return () => onDirty(false);
  }, [guarded, onDirty]);
  useEffect(() => {
    if (!guarded) return;
    const leave = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', leave);
    return () => window.removeEventListener('beforeunload', leave);
  }, [guarded]);
  function issue(e: unknown) {
    if (!alive.current) return;
    if (e instanceof ApiError && e.status === 401) onExpired();
    setError(e instanceof Error ? e.message : uiText('请求未完成。'));
  }
  function valid(v: ComicVersion, n?: number) {
    if (!matchesComic(v, project, n)) throw Error(uiText('分镜本与所选项目或版本不符。'));
    return v;
  }
  function install(v: ComicVersion) {
    setBase(v);
    setDoc(v.document);
    setDirty(false);
    setReady(true);
    setChoice(0);
    setMerge(null);
    setConflict(false);
    setConfirming(false);
  }
  async function history(more = false) {
    const seq = ++listSeq.current;
    try {
      const r = await comicApi.history(project, more ? before : undefined);
      if (
        !Array.isArray(r.items) ||
        r.items.some(
          (v) =>
            v.project_id !== project ||
            !Number.isSafeInteger(v.version) ||
            v.version < 1 ||
            typeof v.confirmed !== 'boolean',
        )
      )
        throw Error(uiText('分镜本版本目录范围不符。'));
      if (alive.current && seq === listSeq.current) {
        setRows((old) => [
          ...new Map([...(more ? old : []), ...r.items].map((v) => [v.version, v])).values(),
        ]);
        setBefore(r.next_before_version || 0);
      }
    } catch (e) {
      if (seq === listSeq.current) issue(e);
    }
  }
  async function first() {
    if (initial.current) return;
    initial.current = true;
    try {
      const v = valid(await comicApi.current(project));
      if (alive.current) install(v);
    } catch (e) {
      if (alive.current && e instanceof ApiError && e.status === 404 && e.code === 'COMIC_NOT_FOUND')
        setReady(true);
      else {
        initial.current = false;
        issue(e);
      }
    }
  }
  useEffect(() => {
    if (enabled && visible) {
      void history();
      void first();
    } else abort.current?.abort();
  }, [enabled, visible, project]);
  async function work(label: string, fn: () => Promise<void>) {
    if (lock.current) return;
    lock.current = true;
    setBusy(label);
    setError('');
    setStatus('');
    try {
      await fn();
    } catch (e) {
      issue(e);
    } finally {
      lock.current = false;
      if (alive.current) setBusy('');
    }
  }
  function edit(next: Comic) {
    setDoc(next);
    setDirty(true);
    setStatus('');
    setConfirming(false);
    setMerge(null);
  }
  const locked = !ready || !!busy || !!pending || blocked;
  function open(n: number) {
    setChoice(0);
    void work(uiText('读取版本'), async () => {
      const v = valid(await comicApi.version(project, n), n);
      if (alive.current) install(v);
    });
  }
  function select(n: number) {
    if (locked || !n) return;
    if (dirty) setChoice(n);
    else open(n);
  }
  function clear(t: ComicTicket) {
    if (readComicTicket(slot)?.key === t.key) sessionStorage.removeItem(slot);
    if (alive.current) setPending(readComicTicket(slot));
  }
  function accept(v: ComicVersion, t: ComicTicket) {
    valid(v, t.expected + (t.operation === 'save' ? 1 : 0));
    if (t.operation === 'confirm' && !v.confirmed) throw Error(uiText('确认回执没有对应作者确认状态。'));
    clear(t);
    if (alive.current) {
      install(v);
      setStatus((t.operation === 'save' ? uiText('已保存 v') : uiText('作者已确认 v')) + v.version);
      void history();
    }
  }
  function mutate(operation: ComicTicket['operation']) {
    if (locked) return;
    if (operation === 'save') {
      const problem = comicProblem(doc);
      if (problem) {
        setError(problem);
        return;
      }
    } else if (!base || base.confirmed || base.version !== latest || dirty || !confirming) return;
    const ticket = { operation, expected: base?.version || 0, key: crypto.randomUUID() };
    void work(operation === 'save' ? uiText('保存版本') : uiText('确认版本'), async () => {
      sessionStorage.setItem(slot, JSON.stringify(ticket));
      setPending(ticket);
      try {
        const v =
          operation === 'save'
            ? await comicApi.save(project, ticket.expected, doc, ticket.key)
            : await comicApi.confirm(project, ticket.expected, ticket.key);
        accept(v, ticket);
      } catch (e) {
        if (e instanceof ApiError && !e.uncertain) {
          clear(ticket);
          if (e.status === 409 && alive.current) {
            setConflict(true);
            setConfirming(false);
            void history();
          }
        }
        throw e;
      }
    });
  }
  function receipt() {
    const t = pending;
    if (!t) return;
    void work(uiText('核对回执'), async () => {
      const v = await comicApi.receipt(project, t.operation, t.key);
      if (v.operation !== t.operation) throw Error(uiText('回执操作与原请求不符。'));
      accept(v, t);
    });
  }
  function reviewLatest() {
    void work(uiText('读取最新版'), async () => {
      const v = valid(await comicApi.current(project));
      if (alive.current) setMerge(v);
    });
  }
  function keepDraft() {
    if (!merge) return;
    setBase(merge);
    setMerge(null);
    setConflict(false);
    setDirty(true);
    setStatus(uiText('已保留本页输入。保存前请复核将提交的所有镜头和设定。'));
  }
  function exportDeck() {
    if (!base?.confirmed || locked || dirty) return;
    const v = base;
    void work('export-pptx', async () => {
      const c = new AbortController();
      abort.current = c;
      try {
        const result = await comicPowerPoint(v, c.signal);
        if (alive.current && !c.signal.aborted) {
          downloadBlob(result.blob, result.name);
          setStatus(uiText('已下载已确认分镜 v') + v.version + uiText(' 的 PowerPoint。'));
        }
      } finally {
        if (abort.current === c) abort.current = null;
      }
    });
  }
  return {
    doc,
    base,
    dirty,
    ready,
    rows,
    before,
    busy,
    error,
    status,
    pending,
    blocked,
    conflict,
    choice,
    confirming,
    merge,
    latest,
    locked,
    edit,
    select,
    open,
    setChoice,
    setConfirming,
    mutate,
    receipt,
    reviewLatest,
    keepDraft,
    install,
    exportDeck,
    cancelExport: () => abort.current?.abort(),
    refresh: () => {
      void history();
      if (!ready) void first();
    },
    more: () => void history(true),
    issue,
  };
}
