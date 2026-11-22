import type { ImageOperation } from './types';

type Draft = { prompt: string; operation: ImageOperation };
type Identity = { owner: string; project: string };

export class AccountDraftHold {
  private previous: Identity | null = null;
  private held: (Identity & Draft) | null = null;

  transition(owner: string, project: string | undefined, restricted: boolean, draft: Draft): Draft | null {
    const previous = this.previous;
    if ((restricted || !owner || !project) && previous?.owner && (!owner || owner === previous.owner) && !this.held) {
      this.held = { ...previous, ...draft };
    }
    const held = this.held;
    const restored = held && held.owner === owner && held.project === project && !restricted ? held : null;
    if (restored || (held && ((owner && owner !== held.owner) || (project && project !== held.project)))) {
      this.held = null;
    }
    if (project) this.previous = { owner, project };
    else if (!owner) this.previous = null;
    return restored ? { prompt: restored.prompt, operation: restored.operation } : null;
  }

  clear() {
    this.held = null;
    this.previous = null;
  }
}
