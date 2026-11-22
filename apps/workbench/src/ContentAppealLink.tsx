import { uiText } from './locale';
import { createContext, useContext } from 'react';
import type { ModerationSummary } from './moderation';
import { validModeration } from './moderation';

export type ContentAppealTarget = { project_id: string; resource_type: 'run' | 'task'; resource_id: string; moderation: ModerationSummary };
type Scope = { project: string; open: (target: ContentAppealTarget) => void };
export const ContentAppealContext = createContext<Scope | null>(null);

export function ContentAppealLink({ type, id, moderation, project, status }: { type: 'run' | 'task'; id?: string | null; moderation?: ModerationSummary; project?: string; status: string }) {
  const scope = useContext(ContentAppealContext);
  if (!scope || !id || status !== 'FAILED' || (project && project !== scope.project) || !validModeration(moderation) || moderation.phase !== 'output' || !['BLOCKED','REVIEW'].includes(moderation.state) || !['policy_block','policy_review','check_unavailable','check_protocol','check_timeout','check_busy','invalid_content','content_too_large','policy_unavailable'].includes(moderation.reason_code || '')) return null;
  return <button type="button" className="text-button" onClick={() => scope.open({ project_id:scope.project, resource_type:type, resource_id:id, moderation })}>{uiText("申请内容申诉")}</button>;
}

