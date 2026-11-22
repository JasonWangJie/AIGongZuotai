// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, expect, it, vi } from 'vitest';
import { AuthGate } from './AuthGate';
import { api, ApiError } from './api';
import { applyLanguage, uiText, useLanguage } from './locale';

afterEach(async () => {
  vi.unstubAllGlobals();
  await applyLanguage('zh-CN', false);
});

it('retranslates an existing validation error without submitting or replacing the form', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  const authenticate = vi.fn();
  function Screen() {
    useLanguage();
    return <AuthGate checking={false} loading={false} busy={false} error="" issue={null}
      onRetry={() => {}} onAuthenticate={authenticate} onForgot={() => {}}/>;
  }
  const host = document.createElement('div');
  const root = createRoot(host);
  try {
    await act(async () => root.render(<Screen/>));
    const form = host.querySelector('form')!;
    await act(async () => { form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); });
    expect(host.querySelector('[role=alert]')?.textContent).toBe('请输入有效邮箱地址。');
    await act(async () => { await applyLanguage('ja', false); });
    expect(host.querySelector('form')).toBe(form);
    const translated = host.querySelector('[role=alert]')?.textContent;
    expect(translated).toBe(uiText('请输入有效邮箱地址。'));
    expect(translated).not.toBe('请输入有效邮箱地址。');
    expect(authenticate).not.toHaveBeenCalled();
  } finally {
    await act(async () => root.unmount());
  }
});

it('reformats known API errors on language changes and preserves unrecognized server text', async () => {
  const message = '邮箱或密码错误';
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(Response.json({error: {code: 'INVALID_CREDENTIALS', message}}, {status: 401}))
    .mockResolvedValueOnce(Response.json({error: {code: 'EXTERNAL_MESSAGE', message}}, {status: 401}));
  vi.stubGlobal('fetch', fetchMock);
  const getError = async () => {
    const error = await api.session().catch(cause => cause);
    expect(error).toBeInstanceOf(ApiError);
    return error as ApiError;
  };
  const known = await getError(), external = await getError();
  await applyLanguage('en', false);
  expect(known.displayMessage).toBe(uiText(message));
  expect(known.displayMessage).not.toBe(message);
  expect(external.displayMessage).toBe(message);
  expect(JSON.stringify(external)).not.toContain(message);
  expect(fetchMock).toHaveBeenCalledTimes(2);
});
