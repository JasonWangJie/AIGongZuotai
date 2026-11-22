import { uiText, currentLocale } from './locale';
import { useLayoutEffect, useRef, useState } from 'react';
import type { KeyboardEvent } from 'react';
import type { AssetEntry } from './libraryState';
import { safeAssetURL } from './api';
import { Icon } from './components';
import { MediaModerationStatus } from './MediaModerationStatus';
import { mediaContentAllowed } from './mediaModeration';

export function AssetThumbnail({ asset }: { asset: AssetEntry }) {
  const [failed, setFailed] = useState(false);
  const url = asset.state === 'active' && mediaContentAllowed(asset.moderation) && asset.thumbnail_url
    ? safeAssetURL(asset.thumbnail_url)
    : undefined;

  if (url && !failed) {
    return <img src={url} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} />;
  }
  const label = asset.state === 'trashed' ? uiText('已回收 · 可恢复')
    : !mediaContentAllowed(asset.moderation) ? uiText('内容暂未开放') : uiText('按需打开原文件');
  return <div className="library-placeholder">
    <Icon name={asset.kind === 'video' ? 'video' : 'image'} size={27} />
    <span>{label}</span>
  </div>;
}

function LibraryCard({ asset, index, open }: { asset: AssetEntry; index: number; open: (asset: AssetEntry) => void }) {
  const kind = asset.source === 'reference' ? uiText('上传参考')
    : asset.kind === 'video' ? uiText('视频结果') : asset.kind === 'poster' ? uiText('静态海报') : uiText('生成图像');
  return <button className="library-card" data-asset-id={asset.id} data-index={index}
    onClick={() => open(asset)} aria-label={uiText('查看素材：') + asset.name}>
    <div className="library-card-media">
      <AssetThumbnail asset={asset} />
      <div className="library-card-badges">
        <span>{kind}</span>
        {asset.provider_mode === 'demo' && <span className="demo-badge">DEMO</span>}
      </div>
    </div>
    <div className="library-card-copy">
      <MediaModerationStatus value={asset.moderation} subject="asset" compact />
      <strong title={asset.name}>{asset.name}</strong>
      <div className="library-card-meta">
        <span>{new Date(asset.created_at).toLocaleDateString(currentLocale())}</span>
        <span>{uiText("元数据 v")}{asset.revision} ↗</span>
      </div>
      {asset.tags.length > 0 && <div className="library-card-tags">
        {asset.tags.slice(0, 4).map(tag => <span key={tag} title={tag}>{tag}</span>)}
      </div>}
    </div>
  </button>;
}

type CardWindow = { start: number; end: number; top: number; bottom: number };

// Keep one spare row on either side. The two spacers also include the row
// gaps they replace, so loading/deleting DOM rows does not move the viewport.
export function libraryCardWindow(count: number, columns: number, stride: number, offset: number, viewport: number): CardWindow {
  const rows = Math.ceil(count / columns);
  const first = Math.min(Math.max(0, rows - 1), Math.max(0, Math.floor(offset / stride) - 1));
  const end = Math.min(rows, Math.max(first + 1, Math.ceil((offset + viewport) / stride) + 1));
  return {
    start: first * columns,
    end: Math.min(count, end * columns),
    top: first * stride,
    bottom: (rows - end) * stride,
  };
}

export function LibraryGrid({ items, open }: { items: AssetEntry[]; open: (asset: AssetEntry) => void }) {
  const grid = useRef<HTMLDivElement>(null);
  const [range, setRange] = useState<CardWindow>({ start: 0, end: 24, top: 0, bottom: 0 });
  const metrics = useRef({ columns: 1, stride: 1 });
  const scrollAnchor = useRef<{ id: string; index: number; rowOffset: number; scrollTop: number } | null>(null);
  const pendingFocus = useRef<number | null>(null);
  const update = useRef<() => void>(() => {});

  useLayoutEffect(() => {
    const element = grid.current;
    const main = element?.closest('main');
    if (!element || !main) return;
    let frame = 0;
    function measure() {
      if (!element || !main) return;
      const card = element.querySelector<HTMLButtonElement>('.library-card');
      if (!card) return;
      const styles = getComputedStyle(element);
      const columns = Math.max(1, styles.gridTemplateColumns.split(/\s+/).length);
      const stride = card.getBoundingClientRect().height + parseFloat(styles.rowGap || '0');
      if (!Number.isFinite(stride) || stride < 1) return;
      const viewportTop = main.getBoundingClientRect().top + main.clientTop;
      let offset = viewportTop - element.getBoundingClientRect().top;
      const previous = scrollAnchor.current;
      if (previous && Math.abs(main.scrollTop - previous.scrollTop) < 1) {
        // A column change, metadata removal, or responsive resize may move a
        // row. Preserve the same visible asset, except during a user's scroll.
        const changedIndex = items[previous.index]?.id !== previous.id;
        const geometryChanged = columns !== metrics.current.columns || Math.abs(stride - metrics.current.stride) > 0.01;
        if (changedIndex || geometryChanged) {
          const index = changedIndex ? items.findIndex(item => item.id === previous.id) : previous.index;
          if (index >= 0) {
            const desired = Math.floor(index / columns) * stride + Math.min(previous.rowOffset, stride - 1);
            main.scrollTop += desired - offset;
            offset = viewportTop - element.getBoundingClientRect().top;
          }
        }
      }
      metrics.current = { columns, stride };
      const firstVisible = Math.floor(offset / stride) * columns;
      scrollAnchor.current = offset >= 0 && firstVisible < items.length
        ? { id: items[firstVisible].id, index: firstVisible, rowOffset: offset % stride, scrollTop: main.scrollTop }
        : null;
      const next = libraryCardWindow(items.length, columns, stride, offset, main.clientHeight);
      setRange(old => old.start === next.start && old.end === next.end && old.top === next.top && old.bottom === next.bottom ? old : next);
    }
    function schedule() {
      if (!frame) frame = requestAnimationFrame(() => { frame = 0; measure(); });
    }
    update.current = measure;
    main.addEventListener('scroll', schedule, { passive: true });
    window.addEventListener('resize', schedule);
    const observer = new ResizeObserver(schedule);
    observer.observe(element);
    observer.observe(main);
    measure();
    return () => {
      main.removeEventListener('scroll', schedule);
      window.removeEventListener('resize', schedule);
      observer.disconnect();
      cancelAnimationFrame(frame);
      update.current = () => {};
    };
  }, [items]);

  useLayoutEffect(() => {
    const index = pendingFocus.current;
    if (index == null) return;
    const next = grid.current?.querySelector<HTMLButtonElement>(`button[data-index="${index}"]`);
    if (!next) return;
    next.focus({ preventScroll: true });
    pendingFocus.current = null;
  }, [range]);

  function moveFocus(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key !== 'Tab' || event.ctrlKey || event.altKey || event.metaKey) return;
    const current = (event.target as HTMLElement).closest<HTMLButtonElement>('button[data-index]');
    const main = grid.current?.closest('main');
    if (!current || !main || !grid.current) return;
    const next = Number(current.dataset.index) + (event.shiftKey ? -1 : 1);
    if (next < 0 || next >= items.length || (next >= range.start && next < range.end)) return;
    event.preventDefault();
    const { columns, stride } = metrics.current;
    const listTop = grid.current.getBoundingClientRect().top - main.getBoundingClientRect().top - main.clientTop;
    pendingFocus.current = next;
    main.scrollTop += listTop + Math.floor(next / columns) * stride;
    update.current();
  }

  // Filtering replaces the collection before the layout effect remeasures it.
  const start = range.start < items.length ? range.start : 0;
  const end = range.start < items.length ? range.end : 24;
  return <div className="library-grid" role="group" aria-label={uiText("当前项目素材")} ref={grid}
    style={{ paddingTop: range.top, paddingBottom: range.bottom }} onKeyDown={moveFocus}>
    {items.slice(start, end).map((asset, offset) => <LibraryCard key={asset.id} asset={asset} index={start + offset} open={open} />)}
  </div>;
}
