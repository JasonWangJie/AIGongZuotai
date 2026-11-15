// @vitest-environment jsdom
import { StrictMode, act } from 'react';
import { createRoot } from 'react-dom/client';
import type { Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from './api';
import { GenerationAccessPage } from './GenerationAccessPage';
import { OperationsGenerationPanel } from './OperationsGenerationPanel';
import { GenerationRecovery } from './generationControls';
import type { GenerationAccess, GenerationAppeal, GenerationPage } from './generationControls';
import type { GenerationAccessController } from './useGenerationAccess';

const open: GenerationAccess = { workspace_id: 'ws_a', state: 'OPEN', version: 1, can_appeal: false };
const restricted: GenerationAccess = { workspace_id: 'ws_a', state: 'RESTRICTED', version: 2, can_appeal: true, restriction: { id: 'grst_a', reason_code: 'manual_review', public_notice: '本轮限制说明', created_at: '2026-11-14T00:00:00Z' } };
const appeal: GenerationAppeal = { id: 'gapl_a', workspace_id: 'ws_a', restriction_id: 'grst_a', state: 'OPEN', version: 1, reason_code: 'request_review', statement: '本页原说明', created_at: '2026-11-14T00:00:00Z', updated_at: '2026-11-14T00:00:00Z' };
const scope = { id: 'ws_a', roles: ['moderator'], permissions: ['read', 'generation_restrict', 'appeal_resolve'] as ('read' | 'generation_restrict' | 'appeal_resolve')[] };
const callbacks = () => ({ onExpired: vi.fn(), onBusy: vi.fn() });
const controller: GenerationAccessController = { data: restricted, error: null, allowed: false, loading: false, updatedAt: 1, refresh: vi.fn(), observe: vi.fn() };
function deferred<T>(signal?: AbortSignal) { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject, signal }; }
let container: HTMLDivElement, root: Root;
beforeEach(() => { vi.useFakeTimers(); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true); container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container); });
afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.restoreAllMocks(); vi.useRealTimers(); vi.unstubAllGlobals(); });
async function render(value: React.ReactNode) { await act(async () => root.render(<StrictMode>{value}</StrictMode>)); }
async function select(value: string) { const element = container.querySelector('select')!; await act(async () => { element.value = value; element.dispatchEvent(new Event('change', { bubbles: true })); }); }
function button(text: string) { const element = [...container.querySelectorAll('button')].find(value => value.textContent === text); expect(element).toBeDefined(); return element!; }

describe('actual React StrictMode reads and superseded requests', () => {
  it('starts a new moderator GET immediately after StrictMode cleanup, without waiting for the old request or 15-second poll', async () => {
    const controls: ReturnType<typeof deferred<GenerationAccess>>[] = [], pages: ReturnType<typeof deferred<GenerationPage<GenerationAppeal>>>[] = [];
    vi.spyOn(api, 'operationGenerationAccess').mockImplementation((_ws, signal) => { const request = deferred<GenerationAccess>(signal); controls.push(request); return request.promise; });
    vi.spyOn(api, 'operationGenerationAppeals').mockImplementation((_q, signal) => { const request = deferred<GenerationPage<GenerationAppeal>>(signal); pages.push(request); return request.promise; });
    await render(<OperationsGenerationPanel owner="u" scope={scope} recovery={new GenerationRecovery()} {...callbacks()}/>);
    expect(controls).toHaveLength(2); expect(pages).toHaveLength(2); expect(controls[0].signal?.aborted).toBe(true); expect(pages[0].signal?.aborted).toBe(true);
    await act(async () => { controls[1].resolve(open); pages[1].resolve({ items: [], next_cursor: null }); });
    expect(container.textContent).toContain('可以发起新生成'); expect(button('刷新当前权限').disabled).toBe(false); expect(vi.getTimerCount()).toBeGreaterThan(0);
  });
  it('starts the user list again immediately after StrictMode abort and publishes the second response', async () => {
    const pages: ReturnType<typeof deferred<GenerationPage<GenerationAppeal>>>[] = [];
    vi.spyOn(api, 'generationAppeals').mockImplementation((_q, signal) => { const request = deferred<GenerationPage<GenerationAppeal>>(signal); pages.push(request); return request.promise; });
    await render(<GenerationAccessPage owner="u" workspace="ws_a" controller={controller} recovery={new GenerationRecovery()} {...callbacks()}/>);
    expect(pages).toHaveLength(2); expect(pages[0].signal?.aborted).toBe(true);
    await act(async () => pages[1].resolve({ items: [appeal], next_cursor: null })); expect(container.textContent).toContain('gapl_a'); expect(button('刷新记录').disabled).toBe(false);
  });
  it('aborts a superseded moderator filter and never lets its late finally release the new read lock', async () => {
    const controls: ReturnType<typeof deferred<GenerationAccess>>[] = [], pages: ReturnType<typeof deferred<GenerationPage<GenerationAppeal>>>[] = [];
    vi.spyOn(api, 'operationGenerationAccess').mockImplementation((_ws, signal) => { const request = deferred<GenerationAccess>(signal); controls.push(request); return request.promise; });
    vi.spyOn(api, 'operationGenerationAppeals').mockImplementation((_q, signal) => { const request = deferred<GenerationPage<GenerationAppeal>>(signal); pages.push(request); return request.promise; });
    await render(<OperationsGenerationPanel owner="u" scope={scope} recovery={new GenerationRecovery()} {...callbacks()}/>); await select('RESOLVED');
    expect(controls).toHaveLength(3); expect(controls[1].signal?.aborted).toBe(true);
    await act(async () => { controls[0].resolve(open); pages[0].resolve({ items: [], next_cursor: null }); controls[1].resolve(open); pages[1].resolve({ items: [appeal], next_cursor: null }); });
    expect(button('正在读取…').disabled).toBe(true); expect(container.textContent).not.toContain('gapl_a');
    await act(async () => { window.dispatchEvent(new Event('focus')); await vi.advanceTimersByTimeAsync(3000); }); expect(controls).toHaveLength(3);
    await act(async () => { controls[2].resolve(restricted); pages[2].resolve({ items: [{ ...appeal, id: 'gapl_current', state: 'RESOLVED', outcome: 'UPHELD', version: 2 }], next_cursor: null }); });
    expect(container.textContent).toContain('新生成暂受限'); expect(container.textContent).toContain('gapl_current'); expect(container.textContent).not.toContain('gapl_a');
  });
  it('ignores an old user/filter response after remounting into another workspace', async () => {
    const pages: ReturnType<typeof deferred<GenerationPage<GenerationAppeal>>>[] = [];
    vi.spyOn(api, 'generationAppeals').mockImplementation((_q, signal) => { const request = deferred<GenerationPage<GenerationAppeal>>(signal); pages.push(request); return request.promise; });
    const recovery = new GenerationRecovery(); await render(<GenerationAccessPage key="u:ws_a" owner="u" workspace="ws_a" controller={controller} recovery={recovery} {...callbacks()}/>);
    await render(<GenerationAccessPage key="other:ws_b" owner="other" workspace="ws_b" controller={{ ...controller, data: { ...open, workspace_id: 'ws_b' } }} recovery={recovery} {...callbacks()}/>);
    expect(pages).toHaveLength(4); expect(pages[1].signal?.aborted).toBe(true);
    await act(async () => { pages[0].resolve({ items: [appeal], next_cursor: null }); pages[1].resolve({ items: [appeal], next_cursor: null }); pages[3].resolve({ items: [{ ...appeal, workspace_id: 'ws_b', id: 'gapl_b' }], next_cursor: null }); });
    expect(container.textContent).toContain('gapl_b'); expect(container.textContent).not.toContain('gapl_a');
  });
  it.each(['success', '401'] as const)('does not let the old mounted appeal POST %s clear a reauthenticated original-key retry', async outcome => {
    vi.spyOn(api, 'generationAppeals').mockResolvedValue({ items: [], next_cursor: null }); const writes: ReturnType<typeof deferred<GenerationAppeal>>[] = [];
    const send = vi.spyOn(api, 'createGenerationAppeal').mockImplementation(() => { const request = deferred<GenerationAppeal>(); writes.push(request); return request.promise; });
    const recovery = new GenerationRecovery(); recovery.rememberAppeal('u', 'ws_a', { restrictionID: 'grst_a', reason: 'request_review', statement: '本页原说明', pending: null });
    await render(<GenerationAccessPage key="first" owner="u" workspace="ws_a" controller={controller} recovery={recovery} {...callbacks()}/>); await act(async () => button('提交复核申请').click()); expect(writes).toHaveLength(1); const oldAttempt = recovery.appeal('u', 'ws_a')?.pending?.attempt;
    await render(<div>会话失效</div>); await render(<GenerationAccessPage key="second" owner="u" workspace="ws_a" controller={controller} recovery={recovery} {...callbacks()}/>); await act(async () => button('按原键核对申请').click()); expect(writes).toHaveLength(2); const retryAttempt = recovery.appeal('u', 'ws_a')?.pending?.attempt; expect(retryAttempt).not.toBe(oldAttempt); expect(send.mock.calls[1]).toEqual(send.mock.calls[0]);
    await act(async () => { if (outcome === 'success') writes[0].resolve(appeal); else writes[0].reject(new ApiError('expired', 401, false, 'UNAUTHORIZED')); });
    expect(recovery.appeal('u', 'ws_a')?.pending?.attempt).toBe(retryAttempt); expect(container.textContent).toContain('原申请的受理结果待核对');
    await act(async () => writes[1].resolve(appeal)); expect(recovery.appeal('u', 'ws_a')).toBeUndefined(); expect(container.textContent).toContain('申请已保存');
  });
});
