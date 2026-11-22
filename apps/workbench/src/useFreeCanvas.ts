import { uiText } from './locale';
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import { mergeCanvas } from './canvasMerge';
import { emptyCanvas, freeCanvasApi, newCanvasID, validCanvas } from './freeCanvas';
import type { CanvasDocument, CanvasRevision, CanvasSources, CanvasVersion } from './freeCanvas';

const receiptsKey = 'studio-free-canvas-pending-1';
let pageReceipts: Record<string, string> = {};
let storageOutOfDate = false;
function receipts(): Record<string, string> {
  if (storageOutOfDate) return pageReceipts;
  try {
    pageReceipts = JSON.parse(sessionStorage.getItem(receiptsKey) || '{}') || {};
  } catch {
    // A subsequent successful write can synchronize the whole page copy.
    storageOutOfDate = true;
  }
  return pageReceipts;
}
function remember(scope: string, key: string) {
  const current = receipts();
  if (key && !current[scope] && Object.keys(current).length >= 16) return false;
  if (key) current[scope] = key;
  else delete current[scope];
  pageReceipts = current;
  try {
    sessionStorage.setItem(receiptsKey, JSON.stringify(current));
    storageOutOfDate = false;
  } catch {
    storageOutOfDate = true;
  }
  return true;
}

function withoutImages(sources: CanvasSources): CanvasSources {
  return Object.fromEntries(Object.entries(sources).filter(([, source]) => !!source.status));
}

export type CanvasProps = {
  project: string;
  owner: string;
  enabled: boolean;
  visible: boolean;
  refreshVersion: number;
  sourceVersion: number;
  assetVersion: number;
  onDirty: (dirty: boolean) => void;
  onExpired: () => void;
};
export function useFreeCanvas(props: CanvasProps) {
  const {
    project,
    owner,
    visible,
    enabled,
    onDirty,
    onExpired,
    refreshVersion,
    sourceVersion,
    assetVersion,
  } = props;
  const scope = JSON.stringify([owner, project]);
  const [initialDocument] = useState(emptyCanvas);
  const [doc, setDoc] = useState<CanvasDocument>(initialDocument);
  const [version, setVersion] = useState<CanvasVersion | null>(null);
  const mergeBase = version?.document ?? initialDocument;
  const [sources, setSources] = useState<CanvasSources>({});
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState<string>(() => receipts()[scope] || '');
  const [busy, setBusy] = useState(false),
    [ready, setReady] = useState(false);
  const [denied, setDenied] = useState(false);
  const [message, setMessage] = useState('');
  const [head, setHead] = useState(0),
    [epoch, setEpoch] = useState(0);
  const [mergeHead, setMergeHead] = useState<CanvasVersion | null>(null);
  const [history, setHistory] = useState<CanvasRevision[]>([]);
  const [before, setBefore] = useState<number>();
  const [historyOpen, setHistoryOpen] = useState(false);
  const primary = useRef<AbortController | null>(null),
    historyRead = useRef<AbortController | null>(null);
  const refreshSources = useRef<AbortController | null>(null);
  const notifySources = useRef<((assets: number, runs: number, installed: number) => void) | null>(null);
  const ref = useRef({ doc, version, dirty, pending, ready, scope, visible, enabled, assetVersion });
  ref.current = { doc, version, dirty, pending, ready, scope, visible, enabled, assetVersion };
  const live = (c: AbortController) =>
    !c.signal.aborted && ref.current.scope === scope && ref.current.visible && ref.current.enabled;
  function install(v: CanvasVersion, checkedAssets?: number) {
    if (v.project_id !== project || !Number.isSafeInteger(v.revision) || v.revision < 1)
      throw Error(uiText('画布版本所属项目不匹配。'));
    if (v.revision !== ref.current.version?.revision) {
      refreshSources.current?.abort();
      setSources(
        checkedAssets === ref.current.assetVersion ? v.sources || {} : withoutImages(v.sources || {}),
      );
    }
    setVersion(v);
    setDoc(v.document);
    setDirty(false);
    setHead((n) => Math.max(n, v.revision));
    setEpoch((n) => n + 1);
    setReady(true);
  }
  function failed(err: unknown) {
    const e = err as ApiError;
    if (e.status === 401) onExpired();
    if (
      e.status === 401 ||
      e.status === 403 ||
      e.code === 'PROJECT_NOT_FOUND' ||
      e.code === 'CANVAS_NOT_FOUND'
    ) {
      primary.current?.abort();
      historyRead.current?.abort();
      refreshSources.current?.abort();
      setMergeHead(null);
      setSources({});
      setHistory([]);
      setVersion(null);
      setDoc(initialDocument);
      setDirty(false);
      setDenied(true);
      setReady(false);
      setMessage(uiText('当前会话已失去此项目的画布权限。'));
    } else setMessage(e.message || uiText('未能读取画布。'));
  }
  async function readVersion(n?: number) {
    if (primary.current || !visible || !enabled || denied || pending) return;
    const c = new AbortController();
    primary.current = c;
    setBusy(true);
    try {
      const v = n
        ? await freeCanvasApi.version(project, n, c.signal)
        : await freeCanvasApi.current(project, c.signal);
      if (live(c)) {
        install(v, assetVersion);
        setMessage(uiText('已读取修订 v') + v.revision + '。');
      }
    } catch (e) {
      if (!live(c)) return;
      if (!n && !ref.current.version && (e as ApiError).code === 'CANVAS_NOT_FOUND') {
        setVersion(null);
        setDoc(initialDocument);
        setDirty(false);
        setSources({});
        setReady(true);
        setMessage('');
      } else failed(e);
    } finally {
      if (primary.current === c) {
        primary.current = null;
        setBusy(false);
      }
    }
  }
  async function previewMerge() {
    if (!visible || !enabled || !dirty || pending || denied || primary.current) return;
    const c = new AbortController();
    primary.current = c;
    setBusy(true);
    try {
      const latest = await freeCanvasApi.current(project, c.signal);
      if (!live(c)) return;
      if (latest.project_id !== project || latest.revision < head)
        throw Error(uiText('最新修订不匹配，请重新核对。'));
      setHead(latest.revision);
      setMergeHead(latest);
    } catch (e) {
      if (live(c)) failed(e);
    } finally {
      if (primary.current === c) {
        primary.current = null;
        setBusy(false);
      }
    }
  }
  function reconcile(prefer: 'local' | 'remote') {
    if (
      !mergeHead ||
      mergeHead.revision !== head ||
      !visible ||
      !enabled ||
      denied ||
      pending ||
      primary.current
    )
      return;
    const result = mergeCanvas(mergeBase, doc, mergeHead.document, prefer);
    install(mergeHead);
    setMergeHead(null);
    setDoc(result.doc);
    setDirty(true);
    setMessage(uiText('已将本页编辑合入修订 v') + mergeHead.revision + uiText('。核对后可显式保存。'));
  }
  const readRef = useRef(readVersion);
  readRef.current = readVersion;
  useEffect(() => {
    if (!enabled || !visible) return;
    if (!ref.current.ready && !ref.current.pending) void readRef.current();
    return () => {
      primary.current?.abort();
      primary.current = null;
      historyRead.current?.abort();
      refreshSources.current?.abort();
      setMergeHead(null);
      setSources({});
      setBusy(false);
    };
  }, [scope, visible, enabled]);
  useEffect(() => {
    onDirty(dirty || !!pending);
    return () => onDirty(false);
  }, [onDirty, dirty, pending]);
  function edit(next: CanvasDocument) {
    if (!visible || !enabled || !ready || primary.current || pending || denied) return;
    setDoc(next);
    setDirty(true);
  }
  async function saveOrCheck(check: boolean) {
    if (!visible || !enabled || denied || primary.current) return;
    if (
      check ? !pending : !!pending || !ready || !dirty || !validCanvas(doc) || head > (version?.revision || 0)
    )
      return;
    const key = check ? pending : newCanvasID('canvas');
    if (!check && !remember(scope, key)) {
      setMessage(uiText('请先核对其他项目尚未确认的画布回执。'));
      return;
    }
    setPending(key);
    const c = new AbortController();
    primary.current = c;
    setBusy(true);
    try {
      const v = check
        ? await freeCanvasApi.receipt(project, key, c.signal)
        : await freeCanvasApi.save(project, version?.revision || 0, doc, key, c.signal);
      if (!live(c)) return;
      install(v, assetVersion);
      remember(scope, '');
      setPending('');
      setMessage(uiText('已确认画布修订 v') + v.revision + '。');
    } catch (err) {
      if (!live(c)) return;
      const e = err as ApiError;
      if (check || e.uncertain || !e.status || e.status >= 500) {
        if (e.status === 401 || e.status === 403 || e.code === 'PROJECT_NOT_FOUND') failed(e);
        else setMessage(check ? uiText('暂未核对到原保存回执，可稍后再次读取。') : uiText('保存回执未确认，请核对原操作。'));
      } else {
        remember(scope, '');
        setPending('');
        failed(e);
        if (e.code === 'CANVAS_CONFLICT') {
          setMessage(uiText('项目中已有新修订。本页编辑已保留，请核对最新修订。'));
          void list(false, false);
        }
      }
    } finally {
      if (primary.current === c) {
        primary.current = null;
        setBusy(false);
      }
    }
  }
  async function list(append: boolean, show = true) {
    if (!visible || !enabled || denied) return;
    historyRead.current?.abort();
    const c = new AbortController();
    historyRead.current = c;
    if (show) setHistoryOpen(true);
    try {
      const page = await freeCanvasApi.revisions(project, append ? before : undefined, c.signal);
      if (!live(c)) return;
      if (!append) setHead((n) => Math.max(n, page.items[0]?.revision || 0));
      if (show) {
        setHistory((old) => (append ? [...old, ...page.items] : page.items));
        setBefore(page.next_before_revision);
      }
    } catch (e) {
      if (live(c)) failed(e);
    }
  }
  const listRef = useRef(list);
  listRef.current = list;
  useEffect(() => {
    if (visible && enabled && ready && !denied) void listRef.current(false, historyOpen);
  }, [refreshVersion, visible, enabled, ready, denied, historyOpen, version?.revision]);
  const bound = !!version?.document.nodes.some((n) => n.asset_id || n.run_id);
  const revision = version?.revision;
  useLayoutEffect(() => {
    if (!visible || !enabled || denied || !revision || !bound) return;
    const c = new AbortController();
    refreshSources.current = c;
    const fixedRevision = revision;
    let lastAssets: number | undefined;
    let lastRuns: number | undefined;
    let lastInstall: number | undefined;
    let reading = false;
    let queued = false;
    async function read() {
      reading = true;
      do {
        queued = false;
        const checkedAssets = lastAssets;
        try {
          const snapshot = await freeCanvasApi.sources(project, fixedRevision, c.signal);
          if (!live(c)) return;
          const next =
            snapshot.project_id === project && snapshot.revision === fixedRevision
              ? snapshot.sources || {}
              : {};
          // Commit the Run progress even if an asset notice arrived mid-read.
          // The follow-up is the first read authorized to restore those images.
          setSources(checkedAssets === lastAssets ? next : withoutImages(next));
        } catch (e) {
          if (!live(c)) return;
          setSources({});
          failed(e);
        }
      } while (queued && live(c));
      reading = false;
    }
    function notify(assets: number, runs: number, installed: number) {
      if (c.signal.aborted || (assets === lastAssets && runs === lastRuns && installed === lastInstall))
        return;
      if (lastAssets !== undefined && assets !== lastAssets) setSources(withoutImages);
      lastAssets = assets;
      lastRuns = runs;
      lastInstall = installed;
      if (reading) queued = true;
      else void read();
    }
    notifySources.current = notify;
    notify(assetVersion, sourceVersion, epoch);
    return () => {
      c.abort();
      notifySources.current = null;
    };
  }, [scope, revision, visible, enabled, denied, bound]);
  useLayoutEffect(() => {
    notifySources.current?.(assetVersion, sourceVersion, epoch);
  }, [assetVersion, sourceVersion, epoch]);
  const closeHistory = useCallback(() => setHistoryOpen(false), []);
  return {
    mergeHead,
    mergeBase,
    previewMerge,
    reconcile,
    cancelMerge: () => setMergeHead(null),
    doc,
    version,
    sources,
    dirty,
    pending,
    busy,
    ready,
    denied,
    message,
    head,
    epoch,
    history,
    before,
    historyOpen,
    edit,
    readVersion,
    save: () => saveOrCheck(false),
    check: () => saveOrCheck(true),
    list,
    closeHistory,
  };
}
