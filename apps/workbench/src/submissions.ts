import type { TaskInput } from './types';

// HTML maxlength counts UTF-16 units. 5,000 units require at most 15,000 UTF-8
// bytes, below the Go API's 16,000-byte prompt cap, including Chinese text.
export const MAX_PROMPT_UNITS = 5000;

export function buildTaskInput(projectId: string, kind: 'image' | 'video', modelId: string, prompt: string, ratio: string, imageCount: number): TaskInput {
  const count = kind === 'video' ? 1 : Number.isInteger(imageCount) && imageCount >= 1 && imageCount <= 4 ? imageCount : 1;
  return { project_id: projectId, kind, model_id: modelId, inputs: { prompt }, parameters: { aspect_ratio: ratio, count } };
}
