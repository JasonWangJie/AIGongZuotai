// @vitest-environment jsdom
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { applyLanguage } from './locale';
import en from './locales/en.json';
import { modelDescription, projectLabel, serverErrorText, workspaceLabel } from './serverDisplay';
import type { Model } from './types';

beforeAll(() => applyLanguage('en', false));
afterAll(() => applyLanguage('zh-CN', false));

describe('server-owned display labels', () => {
  it('keeps project and workspace names outside the reserved demo identities verbatim', () => {
    const project = { id: 'prj_demo', name: '未命名创意项目' };
    const workspace = { id: 'ws_demo', name: '本地开发工作区' };
    expect(projectLabel(project)).toBe(en['未命名创意项目']);
    expect(projectLabel({ ...project, id: 'my-project' })).toBe(project.name);
    expect(workspaceLabel(workspace)).toBe(en['本地开发工作区']);
    expect(workspaceLabel({ ...workspace, id: 'my-workspace' })).toBe(workspace.name);
  });
  it('preserves custom catalog descriptions even when identical to a built-in label', () => {
    const model: Model = { id: 'studio-chat', name: 'Studio Chat', kind: 'chat', description: '本地演示模型 · 不调用真实 AI' };
    expect(modelDescription(model)).toBe(model.description);
    expect(modelDescription({ ...model, builtin_description: true })).toBe(en['本地演示模型 · 不调用真实 AI']);
  });
  it('translates only exact server error messages for the corresponding code', () => {
    expect(serverErrorText('INVALID_CREDENTIALS', '邮箱或密码错误')).toBe(en['邮箱或密码错误']);
    expect(serverErrorText('EXTERNAL_ERROR', '邮箱或密码错误')).toBe('邮箱或密码错误');
    expect(serverErrorText('PROVIDER_STREAM_FAILED', '生成流未完整完成，未自动重新请求模型')).toBe(en['api.PROVIDER_STREAM_FAILED: 生成流未完整完成，未自动重新请求模型']);
    expect(serverErrorText('AUTH_REQUIRED', '用户创建的文本')).toBe('用户创建的文本');
    expect(serverErrorText('toString', '请先登录')).toBe('请先登录');
  });
});
