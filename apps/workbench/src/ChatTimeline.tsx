import { uiText } from './locale';
import { useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { FocusEvent, KeyboardEvent, ReactNode } from 'react';
import { MessageLayout } from './messageLayout';

type Props = {
  ids: readonly string[];
  visible: boolean;
  hasEarlier: boolean;
  loading: boolean;
  activeTail: boolean;
  latestContent?: string;
  latestStatus?: string;
  loadEarlier: () => void;
  renderMessage: (index: number) => ReactNode;
  empty: ReactNode;
  hasBody?: (id: string) => boolean;
  onWindowChange?: (ids: string[]) => void;
  stats?: { indexed: number; resident: number; bytes: number };
};

const actions = 'button:not(:disabled), a[href], input:not(:disabled), summary, [tabindex="0"]';
type Anchor = { id: string; offset: number };

export function ChatTimeline({ ids, visible, hasEarlier, loading, activeTail, latestContent,
  latestStatus, loadEarlier, renderMessage, empty, hasBody, onWindowChange, stats }: Props) {
  const viewport = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const known = useRef(new Map<string, number>());
  const width = useRef(0);
  const [widthRevision, setWidthRevision] = useState(0);
  const model = useMemo(() => new MessageLayout(ids, known.current), [ids, widthRevision]);
  const anchor = useRef<Anchor | null>(null);
  const following = useRef(true);
  const lastAssignedTop = useRef<number | null>(null);
  const [windowIds, setWindowIds] = useState(() => ({
    first: ids[Math.max(0, ids.length - 8)], last: ids.at(-1),
  }));
  const [focused, setFocused] = useState<string | null>(null);
  const focusAfterMount = useRef<{ id: string; lastAction: boolean } | null>(null);
  const readingPage = useRef(false);
  const earlierButton = useRef<HTMLButtonElement>(null);
  const [newContent, setNewContent] = useState(false);
  const scheduler = useRef<() => void>(() => {});
  const latestMeasure = useRef<() => void>(() => {});

  const indices = new Set<number>();
  const first = model.positions.get(windowIds.first) ?? Math.max(0, ids.length - 8);
  const last = model.positions.get(windowIds.last ?? '') ?? ids.length - 1;
  // The production viewport is at most 620px tall; keep a hard bound for
  // synthetic layouts with rows shorter than the workbench action controls.
  for (let i = first; i <= Math.min(last, first + 55); i++) indices.add(i);
  if (activeTail && ids.length) indices.add(ids.length - 1);
  const focusedIndex = focused ? model.positions.get(focused) : undefined;
  if (focusedIndex !== undefined) indices.add(focusedIndex);
  useLayoutEffect(() => {
    if (visible && onWindowChange) onWindowChange([...indices].map(i => ids[i]));
  }, [visible, onWindowChange, ids, windowIds, focused, activeTail]);

  function origin() {
    const node = viewport.current!;
    return list.current!.getBoundingClientRect().top - node.getBoundingClientRect().top + node.scrollTop - node.clientTop;
  }

  function rememberAnchor() {
    if (!viewport.current || !list.current || !ids.length) return;
    const offset = viewport.current.scrollTop - origin();
    const i = model.indexAt(offset);
    anchor.current = { id: ids[i], offset: offset - model.top(i) };
  }

  function positionRows() {
    const node = viewport.current, group = list.current;
    if (!visible || !node || !group || !node.clientHeight) return;
    if (width.current && node.clientWidth !== width.current) {
      // A new line width invalidates every measured wrap, including unmounted rows.
      known.current.clear();
      width.current = node.clientWidth;
      setWidthRevision(n => n + 1);
      return;
    }
    width.current = node.clientWidth;
    const rows = Array.from(group.children) as HTMLElement[];
    for (const row of rows) {
      const i = Number(row.dataset.index);
      const height = row.getBoundingClientRect().height;
      if (!row.hasAttribute('data-body-pending') && ids[i] === row.dataset.messageId && height > 0) {
        known.current.set(ids[i], height);
        model.measure(i, height);
      }
    }
    group.style.height = model.top(ids.length) + 'px';
    for (const row of rows) row.style.top = model.top(Number(row.dataset.index)) + 'px';

    const start = origin();
    const old = anchor.current;
    const anchorIndex = old ? model.positions.get(old.id) : undefined;
    let target = node.scrollTop;
    if (following.current) target = Math.max(0, node.scrollHeight - node.clientHeight);
    else if (old && anchorIndex !== undefined) {
      const rowHeight = model.top(anchorIndex + 1) - model.top(anchorIndex);
      target = start + model.top(anchorIndex) + Math.min(old.offset, rowHeight - 1);
    }
    if (Math.abs(target - node.scrollTop) >= 0.5) {
      node.scrollTop = target;
      lastAssignedTop.current = node.scrollTop;
    }
    rememberAnchor();
    const next = model.range(node.scrollTop - start, node.clientHeight);
    const firstId = ids[next[0]], lastId = ids[next[1] - 1];
    setWindowIds(previous => previous.first === firstId && previous.last === lastId
      ? previous : { first: firstId, last: lastId });
    const pending = focusAfterMount.current;
    if (pending) {
      const row = rows.find(item => item.dataset.messageId === pending.id);
      if (row) {
        focusAfterMount.current = null;
        const target = pending.lastAction ? Array.from(row.querySelectorAll<HTMLElement>(actions)).at(-1) : null;
        (target || row).focus({ preventScroll: true });
      }
    }
  }

  useLayoutEffect(() => {
    latestMeasure.current = positionRows;
    positionRows();
  });
  useLayoutEffect(() => {
    if (!visible || !list.current || !viewport.current) return;
    let frame = 0;
    function schedule() {
      if (!frame) frame = requestAnimationFrame(() => { frame = 0; latestMeasure.current(); });
    }
    scheduler.current = schedule;
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(schedule);
    observer?.observe(viewport.current);
    for (const row of Array.from(list.current.children)) observer?.observe(row);
    window.addEventListener('resize', schedule);
    return () => {
      cancelAnimationFrame(frame);
      observer?.disconnect();
      window.removeEventListener('resize', schedule);
      scheduler.current = () => {};
    };
  }, [visible, model, windowIds, focused, activeTail]);
  useLayoutEffect(() => {
    if (visible && latestContent && !following.current) setNewContent(true);
  }, [visible, latestContent, latestStatus]);

  function handleScroll() {
    const node = viewport.current;
    if (!node || !list.current) return;
    if (lastAssignedTop.current !== node.scrollTop) {
      following.current = node.scrollHeight - node.clientHeight - node.scrollTop < 72;
      if (following.current) setNewContent(false);
      rememberAnchor();
    }
    lastAssignedTop.current = null;
    scheduler.current();
  }

  function handleFocus(event: FocusEvent) {
    const row = (event.target as HTMLElement).closest<HTMLElement>('[data-message-id]');
    if (row?.dataset.messageId) setFocused(row.dataset.messageId);
  }

  function handleKey(event: KeyboardEvent) {
    if (event.key !== 'Tab' || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
    const element = event.target as HTMLElement;
    const row = element.closest<HTMLElement>('[data-message-id]');
    if (!row) return;
    const buttons = Array.from(row.querySelectorAll<HTMLElement>(actions));
    const forward = !event.shiftKey;
    if (element !== (forward ? buttons.at(-1) || row : row)) return;
    const next = Number(row.dataset.index) + (forward ? 1 : -1);
    if (next < 0 || next >= ids.length) return;
    // Native Tab handles adjacent mounted rows. A focused offscreen row can
    // be separated from the viewport by a gap, so render its real neighbor.
    const neighbor = forward ? row.nextElementSibling : row.previousElementSibling;
    if ((neighbor as HTMLElement | null)?.dataset.index === String(next)) return;
    event.preventDefault();
    following.current = false;
    anchor.current = { id: ids[next], offset: 0 };
    focusAfterMount.current = { id: ids[next], lastAction: !forward };
    setFocused(ids[next]);
  }

  function earlier() {
    following.current = false;
    rememberAnchor();
    readingPage.current = document.activeElement === earlierButton.current;
    loadEarlier();
  }
  useLayoutEffect(() => {
    if (!readingPage.current || loading) return;
    readingPage.current = false;
    if (!hasEarlier && document.activeElement === document.body) viewport.current?.focus({ preventScroll: true });
  }, [hasEarlier, loading]);

  return <>
    <div className="chat-timeline chat-window" ref={viewport} role="region" tabIndex={0}
      aria-label={uiText("当前分支消息")} onScroll={handleScroll}
      onWheel={event => {
        const node = event.currentTarget;
        if (event.deltaY < 0 && following.current && node.scrollHeight > node.clientHeight) {
          following.current = false;
          rememberAnchor();
        }
      }}>
      {hasEarlier && <button className="timeline-more button secondary" ref={earlierButton}
        aria-disabled={loading} onClick={() => { if (!loading) earlier(); }}>
        {loading ? uiText('正在读取…') : uiText('加载更早消息')}
      </button>}
      {!ids.length && empty}
      <div ref={list} role="list" aria-label={uiText("已加载的分支消息")} className="chat-window-list"
        data-indexed-count={stats?.indexed} data-resident-count={stats?.resident} data-resident-bytes={stats?.bytes}
        style={{ height: model.top(ids.length) }} onFocusCapture={handleFocus}
        onBlurCapture={e => { if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setFocused(null); }}
        onKeyDown={handleKey}>
        {[...indices].sort((a, b) => a - b).map(i => {
          const pending = !!hasBody && !hasBody(ids[i]);
          return (
          <div key={ids[i]} role="listitem" tabIndex={0} aria-posinset={i + 1} aria-setsize={ids.length}
            data-index={i} data-message-id={ids[i]} className="chat-window-row" data-tail={i === ids.length - 1 || undefined}
            data-body-pending={pending || undefined} aria-busy={pending || undefined}
            style={{ top: model.top(i), height:pending ? model.top(i + 1) - model.top(i) : undefined }}>{renderMessage(i)}</div>);
        })}
      </div>
    </div>
    {newContent && <button className="new-message-button" onClick={() => {
      following.current = true;
      setNewContent(false);
      positionRows();
    }}>{uiText("有新内容 · 回到末尾 ↓")}</button>}
  </>;
}

