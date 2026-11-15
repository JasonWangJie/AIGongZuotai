import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';
import { keepUnknown, requestAdvice } from './requestFeedback';

describe('execution queue rejection preserves manual submission and recovery', () => {
  beforeEach(() => { vi.stubGlobal('window', { setTimeout, clearTimeout }); vi.stubGlobal('fetch', vi.fn()); });
  afterEach(() => vi.unstubAllGlobals());
  function rejected(status: number, code: string) {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({error:{code,message:'private upstream detail',request_id:'req_fixture'}}), {status, headers:{'Retry-After':'3'}}));
  }
  it.each([[503, 'RUN_QUEUE_FULL'], [429, 'EXECUTION_WORKSPACE_QUEUE_LIMIT']] as const)('treats definite new rejection %s/%s as not accepted without retry', async (status, code) => {
    rejected(status, code);
    const input = {project_id:'p1',model_id:'chat',input:'kept input'};
    const error = await api.createRun(input,'original-key').catch(error => error);
    expect(error).toMatchObject({status, code, uncertain:false,requestId:'req_fixture',retryAt:expect.any(Number)});
    expect(error.message).not.toContain('private upstream detail');
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(vi.mocked(fetch).mock.calls[0][1]).toMatchObject({body:JSON.stringify(input),headers:expect.objectContaining({'Idempotency-Key':'original-key'})});
    expect(keepUnknown(true, error)).toBe(true);
    expect(requestAdvice(error, true)).toContain('只核对原请求');
    expect(requestAdvice(error, false)).toContain('不会自动重试');
  });
  it('does not apply the chat pre-admission exception to an unrelated write', async () => {
    rejected(503, 'RUN_QUEUE_FULL');
    await expect(api.saveDraft('p1',{expected_revision:0,nodes:[]},'draft-key')).rejects.toMatchObject({uncertain:true,status:503});
  });
  it('retains uncertainty for malformed or unrelated 5xx failures', async () => {
    rejected(503, 'DATABASE_UNAVAILABLE');
    await expect(api.createRun({project_id:'p1',model_id:'chat',input:'kept'},'original-key')).rejects.toMatchObject({uncertain:true});
    vi.mocked(fetch).mockResolvedValueOnce(new Response('<html>busy</html>',{status:503}));
    await expect(api.createRun({project_id:'p1',model_id:'chat',input:'kept'},'original-key')).rejects.toMatchObject({uncertain:true});
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});
