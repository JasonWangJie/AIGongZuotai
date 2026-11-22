// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
import type { Run } from './types';
import { useStudio } from './useStudio';

class FixtureSource extends EventTarget {
  static opened: FixtureSource[] = [];
  closed = false;
  onerror: (() => void) | null = null;
  constructor(readonly url: string) { super(); FixtureSource.opened.push(this); }
  close() { this.closed = true; }
  emit(name: string, data: { seq: number }) {
    this.dispatchEvent(new MessageEvent(name, { data: JSON.stringify(data), lastEventId: String(data.seq) }));
  }
}

it.each(['completed', 'interrupted', 'failed', 'cancelled'])('resumes the Run from a nonzero cursor and confirms a durable %s event', async outcome => {
  vi.useFakeTimers();
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('EventSource', FixtureSource);
  FixtureSource.opened = [];
  const project = { id: 'p1', name: 'Project' };
  const initial: Run = { id: 'r1', project_id: project.id, model_id: 'm1', prompt: '输入', seq: 451,
    output: '前缀', status: 'RUNNING', created_at: '2026-11-21', updated_at: '2026-11-21' };
  let snapshot = initial;
  let reads = 0;
  const methods: string[] = [];
  vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
    methods.push(init.method || 'GET');
    const path = new URL(url, 'http://localhost').pathname;
    const responses: Record<string, unknown> = {
      '/api/v1/auth/session': { auth_enabled: false, user: null },
      '/api/v1/bootstrap': { workspace: { id: 'w1', name: 'Workspace' }, project, mode: 'demo', storage: 'postgres', models: [] },
      '/api/v1/models': { version: 'legacy', models: [] },
      '/api/v1/tasks': { items: [] }, '/api/v1/runs': { items: [initial] },
    };
    if (path === '/api/v1/runs/r1/snapshot') { reads++; return Response.json(snapshot); }
    if (!(path in responses)) throw new Error('Unexpected API request ' + path);
    return Response.json(responses[path]);
  });
  const view = document.createElement('div');
  const root = createRoot(view);
  function Reader() {
    const { runs } = useStudio();
    const run = runs[0];
    return <output>{run && [run.seq, run.status, run.output, run.updated_at].join('|')}</output>;
  }
  try {
    await act(async () => root.render(<Reader/>));
    const streams = () => FixtureSource.opened.filter(source => source.url.includes('/runs/'));
    const first = streams()[0];
    expect(first.url).toBe('/api/v1/runs/r1/events?after=451&project_id=p1');
    expect(reads).toBe(1);
    const delta = { run_id: initial.id, seq: 452, delta: '🌿' };
    await act(async () => {
      first.emit('message.delta', delta);
      first.onerror?.();
      first.emit('message.delta', delta);
      await vi.advanceTimersByTimeAsync(45);
    });
    expect(view.textContent).toContain('452|RUNNING|前缀🌿|');
    expect(reads).toBe(1);
    expect(first.closed).toBe(false);
    // A real gap requires a fresh snapshot and a new EventSource at its cursor.
    snapshot = { ...initial, seq: 620, output: '补读正文' };
    await act(async () => first.emit('message.delta', { ...delta, seq: 454 }));
    expect(first.closed).toBe(true);
    const second = streams()[1];
    expect(second.url).toBe('/api/v1/runs/r1/events?after=620&project_id=p1');
    expect(reads).toBe(2);
    // An expired window can also supply its complete replacement inline.
    const replacement = { ...initial, seq: 777, output: '窗口外的完整文本' };
    await act(async () => {
      second.emit('run.snapshot', replacement);
      second.emit('message.delta', delta);
      await vi.advanceTimersByTimeAsync(45);
    });
    expect(view.textContent).toContain('777|RUNNING|窗口外的完整文本|');
    expect(reads).toBe(2);
    snapshot = { ...replacement, status: outcome.toUpperCase(), seq: 778, updated_at: 'confirmed by GET' };
    const finalEvent = { run_id: initial.id, seq: snapshot.seq, status: snapshot.status };
    await act(async () => second.emit('run.' + outcome, finalEvent));
    expect(view.textContent).toBe([778, snapshot.status, replacement.output, 'confirmed by GET'].join('|'));
    expect(second.closed).toBe(true);
    expect(reads).toBe(3);
    expect(streams()).toHaveLength(2);
    expect(new Set(methods)).toEqual(new Set(['GET']));
  } finally {
    await act(async () => root.unmount());
    vi.unstubAllGlobals();
    vi.useRealTimers();
  }
});
