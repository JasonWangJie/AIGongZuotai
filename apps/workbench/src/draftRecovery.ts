import { uiText } from './locale';
import { initialDraftState, isDraftDirty } from './draftState';
import type { DraftEditorState } from './draftState';

// Owned by App, never serialized to browser storage. Keys include authenticated user and project.
export class DraftRecoveryStore {
  private drafts = new Map<string, DraftEditorState>();
  private discarded = new Set<string>();
  remember(key: string, state: DraftEditorState, userIntent = false): void {
    if (userIntent) this.discarded.delete(key);
    if (this.discarded.has(key)) return;
    if (isDraftDirty(state)) this.drafts.set(key, state); else this.drafts.delete(key);
  }
  restore(key: string, projectId: string): DraftEditorState {
    this.discarded.delete(key);
    const saved = this.drafts.get(key);
    if (!saved || saved.projectId !== projectId) return initialDraftState(projectId);
    return saved.pending ? { ...saved, phase: 'uncertain', error: uiText('已恢复本页会话内的未确认保存。云端可能已受理，请使用原请求重试；不会创建新的操作键。') } : saved;
  }
  discard(key: string): void { this.drafts.delete(key); this.discarded.add(key); }
  has(key: string): boolean { return this.drafts.has(key); }
  hasUnsaved(): boolean { return this.drafts.size > 0; }
}
