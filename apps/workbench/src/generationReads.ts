export type GenerationReadAttempt = { readonly controller: AbortController };

/** One active read per mounted scope. A cancelled response cannot unlock its replacement. */
export class GenerationReadLane {
  #current: GenerationReadAttempt | null = null;
  get busy() { return this.#current !== null; }
  begin(): GenerationReadAttempt | null { if (this.#current) return null; const attempt = { controller: new AbortController() }; this.#current = attempt; return attempt; }
  current(attempt: GenerationReadAttempt) { return this.#current === attempt && !attempt.controller.signal.aborted; }
  finish(attempt: GenerationReadAttempt) { if (this.#current !== attempt) return false; this.#current = null; return true; }
  cancel() { const attempt = this.#current; this.#current = null; attempt?.controller.abort(); }
}
