// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ChatTimeline } from './ChatTimeline';
import { MessageLayout } from './messageLayout';

describe('variable-height branch history', () => {
  it('maps 10,000 lightweight indices to a bounded range after height revisions', () => {
    const ids = Array.from({length:10000}, (_, i) => String(i));
    const layout = new MessageLayout(ids, new Map());
    const top = 9700 * 128;
    expect(layout.range(top, 640)).toEqual([9695, 9711]);
    layout.measure(17, 2128);
    expect(layout.indexAt(top + 2000)).toBe(9700);
    layout.measure(9700, 32_000);
    expect(layout.range(top + 5000, 640)).toEqual([9700, 9701]);
    expect(layout.top(10000)).toBe(1_313_872);
  });

  let host: HTMLElement;
  let root: ReturnType<typeof createRoot>;
  let frames: Map<number, FrameRequestCallback>;
  let width: number;
  let activeTailHeight: number;
  let stoppedObservers: number;
  const ids = Array.from({length:128}, (_, i) => 'm-' + i);
  const load = vi.fn();

  beforeEach(() => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    host = document.createElement('main');
    document.body.append(host);
    root = createRoot(host);
    frames = new Map();
    let id = 0;
    width = 600;
    activeTailHeight = 240;
    stoppedObservers = 0;
    vi.stubGlobal('requestAnimationFrame', (run: FrameRequestCallback) => { frames.set(++id, run); return id; });
    vi.stubGlobal('cancelAnimationFrame', (n: number) => frames.delete(n));
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      disconnect() { stoppedObservers++; }
    });
    function header() { return host.querySelector('.timeline-more') ? 56 : 16; }
    vi.spyOn(Element.prototype, 'clientHeight', 'get').mockImplementation(() => 320);
    vi.spyOn(Element.prototype, 'clientWidth', 'get').mockImplementation(() => width);
    vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockImplementation(function (this: Element) {
      return header() + (parseFloat(this.querySelector<HTMLElement>('.chat-window-list')?.style.height || '0') || 0) + 16;
    });
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function(this: HTMLElement) {
      const scroll = host.querySelector('.chat-timeline')?.scrollTop || 0;
      const n = Number(this.dataset.messageId?.slice(2));
      if (this.classList.contains('chat-window-list')) return {top:header() - scroll} as DOMRect;
      if (this.dataset.messageId) return {
        top:header() - scroll + parseFloat(this.style.top),
        height:n === 127 ? activeTailHeight : (width < 500 ? 2 : 1) * (80 + (n % 4) * 32),
      } as DOMRect;
      return {top:0, height:320} as DOMRect;
    });
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    load.mockClear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  function render(items: string[], text = 'reply', earlier = false, branch = 'a', visible = true) {
    act(() => root.render(<>
      <ChatTimeline key={branch} ids={items} hasEarlier={earlier} visible={visible} loading={false}
        activeTail latestContent={text} latestStatus="RUNNING" loadEarlier={load} empty={null}
        renderMessage={i => <article><button>{items[i]}</button></article>} />
      <textarea aria-label="草稿" defaultValue="未发送" />
    </>));
  }
  const timeline = () => host.querySelector<HTMLElement>('.chat-timeline')!;
  const row = (id: string) => host.querySelector<HTMLElement>('[data-message-id="' + id + '"]');
  function scroll(top: number) {
    act(() => {
      timeline().scrollTop = top;
      timeline().dispatchEvent(new Event('scroll'));
      for (const [id, frame] of [...frames]) { frames.delete(id); frame(0); }
    });
  }
  function firstVisible() {
    return [...host.querySelectorAll<HTMLElement>('[data-message-id]')].find(el => {
      const r = el.getBoundingClientRect();
      return r.top <= 0 && r.top + r.height > 0;
    })!;
  }

  it('retains the same message and pixel offset across prepend, wrapping and a growing live tail', () => {
    const suffix = ids.slice(64);
    render(suffix, 'a', true);
    scroll(1000);
    const anchor = firstVisible(), initialTop = anchor.getBoundingClientRect().top;
    expect(host.querySelectorAll('[data-message-id]').length).toBeLessThan(20);
    expect(row('m-127')).toBeTruthy();
    const draft = host.querySelector('textarea')!;
    act(() => draft.focus());
    activeTailHeight += 3000;
    render(suffix, 'ab', true);
    expect(firstVisible()).toBe(anchor);
    expect(anchor.getBoundingClientRect().top).toBe(initialTop);
    expect(host.querySelector('.new-message-button')).toBeTruthy();
    render(ids, 'ab', false);
    expect(firstVisible()).toBe(anchor);
    expect(anchor.getBoundingClientRect().top).toBe(initialTop);
    width = 390;
    render(ids, 'abc');
    expect(firstVisible()).toBe(anchor);
    expect(anchor.getBoundingClientRect().top).toBe(initialTop);
    expect(document.activeElement).toBe(draft);
    expect(draft.value).toBe('未发送');
    act(() => host.querySelector<HTMLButtonElement>('.new-message-button')!.click());
    expect(timeline().scrollHeight - timeline().scrollTop - timeline().clientHeight).toBeLessThan(1);
    expect(host.querySelector('.new-message-button')).toBeNull();
  });

  it('pins a focused control and tabs to its real adjacent message across an unmounted gap', () => {
    render(ids);
    scroll(800);
    const first = firstVisible();
    const n = Number(first.dataset.index);
    act(() => first.querySelector('button')!.focus());
    scroll(8800);
    expect(document.activeElement).toBe(first.querySelector('button'));
    expect(row(ids[n + 1])).toBeNull();
    act(() => first.querySelector('button')!.dispatchEvent(new KeyboardEvent('keydown', {key:'Tab', bubbles:true, cancelable:true})));
    expect(document.activeElement).toBe(row(ids[n + 1]));
    expect(host.querySelectorAll('[data-message-id]').length).toBeLessThan(20);
    expect(row(ids[n + 1])!.getAttribute('aria-posinset')).toBe(String(n + 2));
  });

  it('keeps earlier-page loading explicit and resets observers and anchor when the branch changes', () => {
    const old = ids.slice(88);
    render(old, 'a', true);
    scroll(400);
    expect(load).not.toHaveBeenCalled();
    act(() => host.querySelector<HTMLButtonElement>('.timeline-more')!.click());
    expect(load).toHaveBeenCalledOnce();
    render(['m-21', 'm-23'], 'new', false, 'b');
    expect(row('m-127')).toBeNull();
    expect(row('m-23')).toBeTruthy();
    expect(stoppedObservers).toBeGreaterThan(0);
    render(['m-21', 'm-23'], 'new', false, 'b', false);
    expect(frames.size).toBe(0);
  });

  it('holds a cold row at its measured height and requests a stable visible window once across unrelated renders', () => {
    const notify = vi.fn();
    const missing = new Set<string>();
    const draw = () => act(() => root.render(<ChatTimeline ids={ids} visible hasEarlier loading={false}
      activeTail={false} loadEarlier={load} empty={null} onWindowChange={notify}
      stats={{indexed:128, resident:120, bytes:1000}}
      hasBody={id => !missing.has(id)}
      renderMessage={i => missing.has(ids[i]) ? <span className="chat-body-pending">读取中</span> : <article>{ids[i]}</article>} />));
    draw();
    scroll(1200);
    const anchor = firstVisible(), id = anchor.dataset.messageId!;
    const height = anchor.getBoundingClientRect().height, top = anchor.getBoundingClientRect().top;
    const before = notify.mock.calls.length;
    missing.add(id);
    draw();
    expect(row(id)).toBe(anchor);
    expect(anchor.getAttribute('aria-busy')).toBe('true');
    expect(anchor.style.height).toBe(height + 'px');
    missing.clear();
    draw();
    expect(anchor.querySelector('article')?.textContent).toBe(id);
    expect(anchor.style.height).toBe('');
    expect(anchor.getBoundingClientRect().top).toBe(top);
    expect(notify).toHaveBeenCalledTimes(before);
    expect(host.querySelector('.chat-window-list')?.getAttribute('data-resident-count')).toBe('120');
  });
});

