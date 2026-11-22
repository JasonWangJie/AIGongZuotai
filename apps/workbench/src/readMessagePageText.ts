import { uiText } from './locale';
// Read the decoded response stream. Content-Length may be absent or describe
// compressed bytes, so it is only an early rejection, not the final bound.
export async function readMessagePageText(response: Response, limit: number): Promise<string> {
  const reader = response.body?.getReader();
  if (!reader) return '';
  const decoder = new TextDecoder('utf-8', { fatal:true });
  let bytes = 0, text = '';
  try {
    if (Number(response.headers.get('content-length')) > limit) throw new Error(uiText('消息页超过读取预算。'));
    for (;;) {
      const part = await reader.read();
      if (part.done) break;
      bytes += part.value.byteLength;
      if (bytes > limit) throw new Error(uiText('消息页超过读取预算。'));
      text += decoder.decode(part.value, {stream:true});
    }
    return text + decoder.decode();
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally { reader.releaseLock(); }
}

