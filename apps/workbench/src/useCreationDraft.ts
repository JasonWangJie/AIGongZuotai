import { useState } from 'react';
import type { ImageOperation } from './types';

/** Local, incomplete composer input is not an executable or quotable task. */
export function useCreationDraft() {
  const [prompt, setPrompt] = useState('');
  const [imageOperation, setImageOperation] = useState<ImageOperation>('generate');
  return { prompt, setPrompt, imageOperation, setImageOperation };
}
