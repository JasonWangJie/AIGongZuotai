import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { uploadReference } from './referenceUpload';
class FakeXHR {
  static requests: FakeXHR[] = [];
  headers: Record<string, string> = {}; responseHeaders: Record<string, string> = {}; status = 201; responseText = ''; withCredentials = false; timeout = 0; method = ''; url = ''; form?: FormData;
  upload: { onprogress: ((event: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null };
  onload: (() => void) | null = null; onerror: (() => void) | null = null; onabort: (() => void) | null = null; ontimeout: (() => void) | null = null;
  constructor() { FakeXHR.requests.push(this); }
  open(method: string, url: string) { this.method = method; this.url = url; }
  setRequestHeader(key: string, value: string) { this.headers[key] = value; }
  getResponseHeader(key: string) { return this.responseHeaders[key] || null; }
  send(form: FormData) { this.form = form; }
  abort() { this.onabort?.(); }
  respond(body: unknown, status = 201) { this.status = status; this.responseText = JSON.stringify(body); this.onload?.(); }
}
const body = { id: 'r1', project_id: 'p', name: '原图.png', thumbnail_url: '/api/v1/references/r1/thumbnail?project_id=p', url: '/api/v1/references/r1/content?project_id=p' };
describe('actual multipart upload transport', () => {
  beforeEach(() => { FakeXHR.requests = []; vi.stubGlobal('XMLHttpRequest', FakeXHR); }); afterEach(() => vi.unstubAllGlobals());
  it('sends only the original file with project and idempotency headers; 100 percent is not server acceptance', async () => { const file = new File(['abc'], '原图.png', { type: 'image/png' }), progress = vi.fn(); const operation = uploadReference('p', file, 'same-key', progress), xhr = FakeXHR.requests[0]; let done = false; void operation.promise.then(() => { done = true; }); expect(xhr.url).toBe('/api/v1/references'); expect(xhr.headers).toEqual({ 'X-Project-ID': 'p', 'Idempotency-Key': 'same-key' }); expect([...xhr.form!.keys()]).toEqual(['file']); expect((xhr.form!.get('file') as File).name).toBe(file.name); expect(await (xhr.form!.get('file') as File).text()).toBe('abc'); expect(xhr.withCredentials).toBe(true); xhr.upload.onprogress?.({ lengthComputable: true, loaded: 10, total: 10 }); await Promise.resolve(); expect(progress).toHaveBeenCalledWith(100); expect(done).toBe(false); xhr.respond(body); await expect(operation.promise).resolves.toEqual(body); });
  it('keeps abort unknown and permits explicit same-file same-key replay', async () => { const file = new File(['abc'], '原图.png'), first = uploadReference('p', file, 'original-key', () => {}); first.abort(); await expect(first.promise).rejects.toMatchObject({ status: 0, uncertain: true }); const second = uploadReference('p', file, 'original-key', () => {}); expect(FakeXHR.requests[1].headers).toEqual(FakeXHR.requests[0].headers); expect(await (FakeXHR.requests[1].form!.get('file') as File).text()).toBe('abc'); FakeXHR.requests[1].respond(body); await second.promise; });
  it('rejects a response belonging to another project as unknown', async () => { const operation = uploadReference('p', new File(['a'], 'x.png'), 'key', () => {}); FakeXHR.requests[0].respond({ ...body, project_id: 'other' }); await expect(operation.promise).rejects.toMatchObject({ uncertain: true }); });
  it('extracts definitive structured admission rejection and diagnostic cooldown', async () => { const operation = uploadReference('p', new File(['a'], 'x.png'), 'key', () => {}), xhr = FakeXHR.requests[0]; xhr.responseHeaders = { 'Retry-After': '3', 'X-Request-ID': 'req_upload' }; xhr.respond({ error: { code: 'REFERENCE_PREPARATION_BUSY', message: '稍后重试' } }, 429); await expect(operation.promise).rejects.toMatchObject({ uncertain: false, status: 429, requestId: 'req_upload', retryAt: expect.any(Number) }); });
  it('retains uncertainty for malformed success or intermediary errors', async () => { const operation = uploadReference('p', new File(['a'], 'x.png'), 'key', () => {}), xhr = FakeXHR.requests[0]; xhr.status = 503; xhr.responseText = '<html>offline</html>'; xhr.onload?.(); await expect(operation.promise).rejects.toMatchObject({ uncertain: true, status: 503 }); });
  it('rejects a JSON null response without leaving the upload promise hanging', async () => { const operation = uploadReference('p', new File(['a'], 'x.png'), 'key', () => {}); FakeXHR.requests[0].respond(null); await expect(operation.promise).rejects.toMatchObject({ uncertain: true }); });
});
