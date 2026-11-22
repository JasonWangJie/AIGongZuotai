// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LibraryGrid, libraryCardWindow } from './LibraryGrid';
import { LibraryAssetPreview, LibraryDetail, libraryPlaybackToken } from './LibraryDetail';
import { LibraryRecovery } from './libraryState';
import type { AssetEntry } from './libraryState';
import { api } from './api';

function asset(index: number): AssetEntry {
  return {
    id: 'asset-' + index, project_id: 'p', source: 'generated', kind: 'image',
    name: '素材 ' + index, tags: [], state: 'active', revision: 1,
    created_at: '2026-01-01T00:00:00Z', updated_at: '', deleted_at: null,
    content_url: '/api/v1/library/asset-' + index + '/content',
    thumbnail_url: '/api/v1/library/asset-' + index + '/thumbnail',
  };
}

describe('作品库资源随视口和项目退出', () => {
  let main: HTMLElement;
  let root: ReturnType<typeof createRoot>;
  let frames: Map<number, FrameRequestCallback>;
  let disconnected: ReturnType<typeof vi.fn>;
  const nativeDialog = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'showModal');

  beforeEach(() => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    main = document.createElement('main');
    document.body.append(main);
    root = createRoot(main);
    frames = new Map();
    let id = 0;
    vi.stubGlobal('requestAnimationFrame', (run: FrameRequestCallback) => { frames.set(++id, run); return id; });
    vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id));
    disconnected = vi.fn();
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect = disconnected; });
    Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value() {} });
  });
  afterEach(() => {
    act(() => root.unmount());
    main.remove();
    if (nativeDialog) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', nativeDialog);
    else Reflect.deleteProperty(HTMLDialogElement.prototype, 'showModal');
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('opens a deep window in ten thousand ordered indexes and reaches the exact partial last row', () => {
    const result = libraryCardWindow(10000, 3, 200, 480000, 840);
    expect(result).toEqual({ start: 7197, end: 7218, top: 479800, bottom: 185600 });
    const last = libraryCardWindow(10000, 3, 200, 667000, 840);
    expect(last.end).toBe(10000);
    expect(last.bottom).toBe(0);
    expect(last.start).toBeLessThan(10000);
  });

  it('keeps the deep anchor after appending, moves keyboard focus across windows and disconnects observers', () => {
    Object.defineProperty(main, 'clientHeight', { value: 300 });
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const top = this.classList.contains('library-grid') ? -main.scrollTop : 0;
      return { top, height: 100 } as DOMRect;
    });
    let columns = '200px 200px';
    vi.spyOn(window, 'getComputedStyle').mockImplementation(() => ({ gridTemplateColumns: columns, rowGap: '10px' } as CSSStyleDeclaration));
    const entries = Array.from({ length: 245 }, (_, i) => asset(i));
    function render(count: number) { act(() => root.render(<LibraryGrid items={entries.slice(0, count)} open={() => {}} />)); }
    function scroll(offset: number) {
      act(() => {
        main.scrollTop = offset;
        main.dispatchEvent(new Event('scroll'));
        for (const run of [...frames.values()]) run(0);
        frames.clear();
      });
    }
    render(240);
    scroll(11000);
    const anchor = main.querySelector('[data-index="200"]');
    expect(anchor?.textContent).toContain('素材 200');
    expect(main.querySelectorAll('.library-card').length).toBeLessThan(15);
    const before = main.querySelector<HTMLElement>('.library-grid')!.style.paddingTop;
    render(245);
    expect(main.scrollTop).toBe(11000);
    expect(main.querySelector('[data-index="200"]')).toBe(anchor);
    expect(main.querySelector<HTMLElement>('.library-grid')!.style.paddingTop).toBe(before);
    columns = '160px 160px 160px';
    act(() => {
      window.dispatchEvent(new Event('resize'));
      for (const run of [...frames.values()]) run(0);
      frames.clear();
    });
    expect(main.scrollTop).toBe(66 * 110);
    expect(main.querySelector('[data-index="200"]')).toBe(anchor);
    const last = [...main.querySelectorAll<HTMLButtonElement>('button')].at(-1)!;
    const target = Number(last.dataset.index) + 1;
    act(() => {
      last.focus();
      last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
    });
    expect((document.activeElement as HTMLElement).dataset.index).toBe(String(target));
    scroll(0);
    expect(main.querySelector('[data-index="200"]')).toBeNull();
    act(() => root.render(null));
    expect(disconnected).toHaveBeenCalled();
    expect(frames.size).toBe(0);
  });

  it('closes the exact player and clears its source when current access disappears', () => {
    const pause = vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {});
    const load = vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {});
    const video = { ...asset(1), kind: 'video' as const };
    act(() => root.render(<LibraryAssetPreview asset={video} playing={libraryPlaybackToken(video)} onPlay={() => {}} />));
    const player = main.querySelector('video')!;
    expect(player.getAttribute('src')).toBe(video.content_url);
    act(() => root.render(<LibraryAssetPreview asset={{ ...video, state: 'trashed' }} playing={libraryPlaybackToken(video)} onPlay={() => {}} />));
    expect(main.querySelector('video')).toBeNull();
    expect(player.hasAttribute('src')).toBe(false);
    expect(pause).toHaveBeenCalledTimes(1);
    expect(load).toHaveBeenCalledTimes(1);
  });

  it('aborts outstanding version and revision reads on detail exit', async () => {
    let detailSignal: AbortSignal | undefined;
    let historySignal: AbortSignal | undefined;
    const stopped = (signal?: AbortSignal) => new Promise<never>((_, reject) => signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true }));
    vi.spyOn(api, 'libraryAsset').mockImplementation((_project, _id, signal) => { detailSignal = signal; return stopped(signal); });
    vi.spyOn(api, 'libraryRevisions').mockImplementation((_project, _id, _before, signal) => { historySignal = signal; return stopped(signal); });
    act(() => root.render(<LibraryDetail asset={asset(1)} owner="u" project="p" recovery={new LibraryRecovery()} onClose={() => {}} onChanged={() => {}} onAuthExpired={() => {}} />));
    const history = [...main.querySelectorAll('button')].find(button => button.textContent === '刷新版本历史')!;
    act(() => history.click());
    expect(detailSignal?.aborted).toBe(false);
    expect(historySignal?.aborted).toBe(false);
    await act(async () => root.render(null));
    expect(detailSignal?.aborted).toBe(true);
    expect(historySignal?.aborted).toBe(true);
  });
});
