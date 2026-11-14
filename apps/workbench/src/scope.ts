export type ScopeToken = { revision: number; projectId: string | null };

// Every navigation/logout invalidates all previous requests, including A → B → A.
export class RequestScope {
  private revision = 0;
  private projectId: string | null = null;
  transition(projectId: string | null): ScopeToken { this.revision++; this.projectId = projectId; return this.capture(); }
  capture(): ScopeToken { return { revision: this.revision, projectId: this.projectId }; }
  current(token: ScopeToken): boolean { return token.revision === this.revision && token.projectId === this.projectId; }
  bind(token: ScopeToken, projectId: string): ScopeToken | null { if (!this.current(token)) return null; this.projectId = projectId; return this.capture(); }
  accepts(token: ScopeToken, resource: { project_id: string }): boolean { return this.current(token) && token.projectId !== null && resource.project_id === token.projectId; }
}
