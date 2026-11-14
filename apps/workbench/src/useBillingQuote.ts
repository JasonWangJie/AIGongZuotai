import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import type { Bootstrap } from './types';
import { billingEnabled, billingScope, quoteMatches, quoteResponseMatches, quoteSignature } from './billing';
import type { QuoteRequest, QuotedRequest } from './billing';

export function useBillingQuote(bootstrap: Bootstrap | null, request: QuoteRequest | null, onExpired: () => void) {
  const scope = billingScope(bootstrap), signature = quoteSignature(scope, request), enabled = billingEnabled(bootstrap);
  const current = useRef(signature); current.current = signature;
  const generation = useRef(0), controller = useRef<AbortController | null>(null), locked = useRef(false);
  const [accepted, setAccepted] = useState<QuotedRequest | null>(null), [loading, setLoading] = useState(false), [error, setError] = useState<ApiError | null>(null), [now, setNow] = useState(Date.now());
  useEffect(() => { generation.current++; controller.current?.abort(); locked.current = false; setLoading(false); setAccepted(null); setError(null); return () => { generation.current++; controller.current?.abort(); }; }, [signature, enabled]);
  useEffect(() => { if (!accepted) return; const timer = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(timer); }, [accepted]);
  async function fetchQuote() {
    if (!enabled || !bootstrap || !request || locked.current || Date.now() < (error?.retryAt || 0)) return;
    locked.current = true; const ticket = ++generation.current, sentSignature = signature, body = structuredClone(request), identity = bootstrap;
    controller.current?.abort(); controller.current = new AbortController(); setLoading(true); setError(null); setAccepted(null);
    try {
      const quote = await api.billingQuote(identity.project.id, body, controller.current.signal);
      if (ticket !== generation.current || current.current !== sentSignature) return;
      if (!quoteResponseMatches(quote, body, identity)) throw new ApiError('报价身份或计量单位不匹配，请刷新后重新获取。');
      setNow(Date.now()); setAccepted({ scope, request: body, quote });
    } catch (cause) { if (ticket !== generation.current || current.current !== sentSignature) return; const issue = cause instanceof ApiError ? cause : new ApiError('暂时无法取得报价。'); setError(issue); if (issue.status === 401) onExpired(); }
    finally { if (ticket === generation.current && current.current === sentSignature) { locked.current = false; setLoading(false); } }
  }
  const visible = accepted && accepted.scope === scope && quoteSignature(scope, accepted.request) === signature ? accepted : null;
  return { accepted: visible, valid: !!request && quoteMatches(visible, scope, request, now), loading, error, fetchQuote, clear: () => { generation.current++; controller.current?.abort(); locked.current = false; setAccepted(null); setLoading(false); setError(null); } };
}
