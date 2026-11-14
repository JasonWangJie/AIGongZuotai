import { useEffect, useState } from 'react';
import { MAX_RETRY_SECONDS, remainingSeconds } from './requestFeedback';

export function useCountdown(deadline: number, maximumSeconds = MAX_RETRY_SECONDS): number {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    let timer: number | undefined;
    const update = () => { window.clearTimeout(timer); const time = Date.now(); setNow(time); if (document.visibilityState !== 'hidden' && remainingSeconds(deadline, time, maximumSeconds) > 0) timer = window.setTimeout(update, 1000); };
    update(); document.addEventListener('visibilitychange', update);
    return () => { window.clearTimeout(timer); document.removeEventListener('visibilitychange', update); };
  }, [deadline, maximumSeconds]);
  return remainingSeconds(deadline, now, maximumSeconds);
}
