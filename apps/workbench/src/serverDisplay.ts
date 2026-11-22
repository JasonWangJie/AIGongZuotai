import { catalogText, uiText } from './locale';
import type { Model, Project } from './types';

export function workspaceLabel(workspace?: { id: string; name: string }) {
  return workspace?.id === 'ws_demo' && workspace.name === '本地开发工作区'
    ? uiText('本地开发工作区') : workspace?.name;
}

export function projectLabel(project?: Project) {
  return project?.id === 'prj_demo' && project.name === '未命名创意项目'
    ? uiText('未命名创意项目') : project?.name;
}

export function modelDescription(model?: Model) {
  if (model?.builtin_description) {
    if (model.description === '本地演示模型 · 不调用真实 AI') return uiText('本地演示模型 · 不调用真实 AI');
    if (model.description === '已配置的服务端模型 · 请求可能计费') return uiText('已配置的服务端模型 · 请求可能计费');
  }
  return model?.description;
}

const gatewayDefaults: Readonly<Record<string, string>> = {
  'api.UPSTREAM_GENERATION_FAILED: gateway reported a terminal generation failure; no automatic resubmission was made': '网关报告生成失败，未自动重新提交。',
  'api.PARTIAL_OUTPUT: gateway completed with fewer usable results than requested': '网关返回的可用结果少于请求数量。',
};

// A translated API error needs both a recognized code and the exact platform
// message. Dynamic provider details pass through unchanged.
export function serverErrorText(code: string | undefined, message: unknown) {
  if (typeof message !== 'string') return undefined;
  if (!code || code.length > 128 || message.length > 1024) return message;
  const key = 'api.' + code + ': ' + message;
  return catalogText(key, Object.hasOwn(gatewayDefaults, key) ? gatewayDefaults[key] : message);
}
