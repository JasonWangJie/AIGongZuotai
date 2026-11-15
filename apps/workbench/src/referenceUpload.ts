import { ApiError } from './api';
import { retryDeadline, safeRequestId } from './requestFeedback';
import type { ReferenceAsset } from './types';
import { definiteModerationUnavailable, mediaContentAllowed, mediaModerationErrorMessage } from './mediaModeration';

export function uploadReference(projectId: string, file: File, key: string, onProgress: (progress: number | null) => void): { promise: Promise<ReferenceAsset>; abort: () => void } {
  const xhr = new XMLHttpRequest();
  const promise = new Promise<ReferenceAsset>((resolve, reject) => {
    let settled = false;
    const finish = (error?: ApiError, asset?: ReferenceAsset) => { if (settled) return; settled = true; xhr.onload = xhr.onerror = xhr.onabort = xhr.ontimeout = null; xhr.upload.onprogress = null; if (error) reject(error); else resolve(asset!); };
    xhr.open('POST', '/api/v1/references'); xhr.withCredentials = true; xhr.timeout = 120000;
    xhr.setRequestHeader('X-Project-ID', projectId); xhr.setRequestHeader('Idempotency-Key', key);
    xhr.upload.onprogress = event => onProgress(event.lengthComputable && event.total > 0 ? Math.max(0, Math.min(100, Math.floor(event.loaded / event.total * 100))) : null);
    xhr.onload = () => {
      const requestId = safeRequestId(xhr.getResponseHeader('X-Request-ID')), retryAt = retryDeadline(xhr.getResponseHeader('Retry-After'), xhr.status);
      let body: ReferenceAsset & { error?: { code?: string; message?: string; request_id?: string } };
      try { body = JSON.parse(xhr.responseText); } catch { finish(new ApiError('上传响应无法识别，文件可能已经保存，请用原文件重试核对。', xhr.status, true, undefined, requestId, retryAt)); return; }
      if (!body || typeof body !== 'object' || Array.isArray(body)) { finish(new ApiError('上传响应内容无效，保留原文件与请求等待重试核对。', xhr.status, true, undefined, requestId, retryAt)); return; }
      if (xhr.status < 200 || xhr.status >= 300) { const code = typeof body.error?.code === 'string' ? body.error.code : undefined; finish(new ApiError(mediaModerationErrorMessage(code) || body.error?.message || `上传失败（${xhr.status}）`, xhr.status, xhr.status >= 500 && !definiteModerationUnavailable(xhr.status, code), code, requestId || body.error?.request_id, retryAt)); return; }
      if (!body.id || body.project_id !== projectId || !body.thumbnail_url || !body.url) { finish(new ApiError('上传结果无法核对，保留原文件与请求等待重试。', xhr.status, true, undefined, requestId)); return; }
      if (!mediaContentAllowed(body.moderation)) { finish(new ApiError('上传返回了未开放的内容，保存结果需核对。请保留原文件与原请求键，不要重新创建上传。', xhr.status, true, 'MEDIA_MODERATION_RESPONSE_INVALID', requestId)); return; }
      finish(undefined, body);
    };
    xhr.onerror = () => finish(new ApiError('上传连接中断，保存状态待确认，请用原文件重试。', 0, true));
    xhr.ontimeout = () => finish(new ApiError('上传等待超时，文件可能已经保存，请用原文件重试核对。', 0, true));
    xhr.onabort = () => finish(new ApiError('已停止传输，服务器保存状态仍需核对；这不等于删除已上传文件。', 0, true));
    const form = new FormData(); form.append('file', file, file.name); xhr.send(form);
  });
  return { promise, abort: () => xhr.abort() };
}
