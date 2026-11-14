import { billingEnabled, billingScope, quoteMatches } from './billing';
import type { QuoteRequest, QuotedRequest } from './billing';
import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { applyRunEvent, mergeRun, mergeTask } from './state';
import { terminal } from './types';
import { moderationMediaBlocked, moderationErrorMessage } from './moderation';
import { buildModelTaskInput, CatalogCache, catalogScope, modelCapability, promptBytes, validCatalog } from './modelCatalog';
import type { ModelSelection } from './modelCatalog';
import { RequestScope } from './scope';
import { browserProjectStorage, readProjectPreference, rememberProject } from './projectPreference';
import { PendingRequests, SubmissionFlights } from './pendingRequests';
import type { PendingOperation } from './pendingRequests';
import { keepUnknown } from './requestFeedback';
import type { ResourceHint } from './durableEvents';
import { boundedReads, DurableFeedSync, parseResourceChanged, resourceHintCovered, validStreamReset } from './durableEvents';
import type { ScopeToken } from './scope';
import type { AuthSession, Bootstrap, Project, Run, Task, ImageOperation } from './types';

type Pending = PendingOperation;
const unknownMessage = '本次请求可能已受理；请刷新当前项目历史核对，或使用“原请求重试”，不会创建新的操作键。';

export function useStudio() {
  const [bootstrap, setBootstrap] = useState<Bootstrap | null>(null);
  const [session, setSession] = useState<AuthSession | null>(null);
  const [authState, setAuthState] = useState<'checking' | 'required' | 'ready'>('checking');
  const [authBusy, setAuthBusy] = useState(false);
  const [authError, setAuthError] = useState('');
  const [authIssue, setAuthIssue] = useState<ApiError | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [selectedRun, setSelectedRun] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setErrorText] = useState('');
  const [requestError, setRequestError] = useState<ApiError | null>(null);
  const [submissionRetryAt, setSubmissionRetryAt] = useState(0);
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [catalogError, setCatalogError] = useState<ApiError | null>(null);
  const [catalogUpdatedAt, setCatalogUpdatedAt] = useState(0);
  const setError = useCallback((message: string) => { setErrorText(message); setRequestError(null); }, []);
  const [pending, setPending] = useState<Pending | null>(null);
  const [taskConnection, setTaskConnection] = useState('连接中');
  const [runConnection, setRunConnection] = useState('');
  const [reconnect, setReconnect] = useState(0);
  const [updates, setUpdates] = useState({ activity: 0, library: 0, references: 0, draft: 0, conversations: 0 });
  const [syncNotice, setSyncNotice] = useState('');
  const feedSync = useRef<DurableFeedSync | null>(null), tasksRef = useRef<Task[]>([]), selectedRunRef = useRef(selectedRun); tasksRef.current = tasks; selectedRunRef.current = selectedRun;
  const historyFlights = useRef(new Map<number, Promise<void>>()), historyAbort = useRef(new AbortController());
  const [historyOffsets, setHistoryOffsets] = useState<{ tasks: number | null; runs: number | null }>({ tasks: null, runs: null });
  const [historyLoading, setHistoryLoading] = useState({ tasks: false, runs: false });
  const historyExtended = useRef(new Set<'tasks' | 'runs'>());
  const historyLocks = useRef(new Map<'tasks' | 'runs', number>());
  const scope = useRef(new RequestScope());
  const streams = useRef(new Set<EventSource>());
  const runsRef = useRef<Run[]>([]);
  const bootstrapRef = useRef<Bootstrap | null>(null);
  const pendingByProject = useRef(new PendingRequests());
  const inflight = useRef(new SubmissionFlights());
  const authLock = useRef(false);
  const alive = useRef(true);
  const lastSessionCheck = useRef(0);
  const catalogCache = useRef(new CatalogCache());
  const catalogAbort = useRef<AbortController | null>(null), catalogSequence = useRef(0), catalogLock = useRef(false);
  const catalogRetryAt = useRef(0);
  const current = useCallback((token: ScopeToken) => alive.current && scope.current.current(token), []);
  const closeStreams = useCallback(() => { streams.current.forEach(stream => stream.close()); streams.current.clear(); }, []);
  const clearProject = useCallback(() => {
    closeStreams(); feedSync.current?.stop(); feedSync.current = null; historyAbort.current.abort(); historyAbort.current = new AbortController(); historyFlights.current.clear(); tasksRef.current = []; bootstrapRef.current = null; runsRef.current = []; setUpdates({ activity: 0, library: 0, references: 0, draft: 0, conversations: 0 }); setSyncNotice('');
    catalogSequence.current++; catalogAbort.current?.abort(); catalogLock.current = false; catalogRetryAt.current = 0; setCatalogLoading(false); setCatalogError(null); setCatalogUpdatedAt(0);
    setBootstrap(null); setTasks([]); setRuns([]); setSelectedRun(null); setPending(null); setSubmitting(false);
    setTaskConnection('连接中'); setRunConnection(''); setError(''); setSubmissionRetryAt(0);
    historyExtended.current.clear(); historyLocks.current.clear(); setHistoryOffsets({ tasks: null, runs: null }); setHistoryLoading({ tasks: false, runs: false });
  }, [closeStreams]);
  const expireSession = useCallback((message = '会话已过期，请重新登录。') => {
    scope.current.transition(null); clearProject(); inflight.current.clear(); catalogCache.current.clear();
    setProjects([]); setSession(old => ({ auth_enabled: true, user: null, account_security: old?.account_security })); setAuthState('required'); setAuthError(message); setLoading(false);
  }, [clearProject]);
  const failure = useCallback((cause: unknown, token?: ScopeToken) => {
    if (token && !current(token)) return;
    if (cause instanceof ApiError && cause.status === 401) { setAuthIssue(cause); expireSession(); return; }
    setErrorText(cause instanceof Error ? cause.message : '请求失败，请重试。'); setRequestError(cause instanceof ApiError ? cause : null);
  }, [current, expireSession]);
  const updateRun = useCallback((run: Run, token: ScopeToken) => {
    if (!alive.current || !scope.current.accepts(token, run)) return;
    const next = mergeRun(runsRef.current, run); runsRef.current = next; setRuns(next); const applied = next.find(item => item.id === run.id)!; feedSync.current?.acknowledge({ type: 'run', id: applied.id, version: applied.seq, status: applied.status });
  }, []);
  const readHistoryFor = useCallback(async (token: ScopeToken) => {
    if (!token.projectId) return;
    const results = await Promise.allSettled([api.tasks(token.projectId, 0, historyAbort.current.signal), api.runs(token.projectId, 0, historyAbort.current.signal)]);
    if (!current(token)) return;
    const failed = results.find(result => result.status === 'rejected');
    const unauthorized = results.find(result => result.status === 'rejected' && result.reason instanceof ApiError && result.reason.status === 401);
    if (unauthorized?.status === 'rejected') { failure(unauthorized.reason, token); return; }
    if (results[0].status === 'fulfilled') {
      const page = results[0].value;
      const incoming = page.items.filter(task => scope.current.accepts(token, task));
      setTasks(old => current(token) ? incoming.reduce(mergeTask, old) : old);
      if (!historyExtended.current.has('tasks')) setHistoryOffsets(old => ({ ...old, tasks: page.has_more ? (page.offset || 0) + page.items.length : null }));
    }
    if (results[1].status === 'fulfilled') {
      const page = results[1].value;
      const next = page.items.filter(run => scope.current.accepts(token, run)).reduce(mergeRun, runsRef.current);
      runsRef.current = next; setRuns(next); setSelectedRun(old => old || next[0]?.id || null);
      if (!historyExtended.current.has('runs')) setHistoryOffsets(old => ({ ...old, runs: page.has_more ? (page.offset || 0) + page.items.length : null }));
    }
    if (failed?.status === 'rejected') throw failed.reason;
  }, [current, failure]);
  const refreshFor = useCallback((token: ScopeToken): Promise<void> => {
    const existing = historyFlights.current.get(token.revision); if (existing) return existing;
    const read = readHistoryFor(token).finally(() => { if (historyFlights.current.get(token.revision) === read) historyFlights.current.delete(token.revision); }); historyFlights.current.set(token.revision, read); return read;
  }, [readHistoryFor]);
  const loadProject = useCallback(async (projectId?: string) => {
    let token = scope.current.transition(projectId || null);
    clearProject(); setLoading(true);
    try {
      const data = await api.bootstrap(projectId);
      if (!current(token)) return;
      if (data.catalog_version && !validCatalog({ version: data.catalog_version, models: data.models })) throw new ApiError('模型目录格式不完整，已阻止使用未知参数提交。');
      if (projectId && data.project.id !== projectId) throw new Error('服务端返回了不同项目，已阻止加载。');
      const bound = scope.current.bind(token, data.project.id); if (!bound) return;
      token = bound; bootstrapRef.current = data; setBootstrap(data); setProjects(data.projects || [data.project]);
      catalogCache.current.seed(catalogScope(data.user?.id || 'demo', data.workspace.id), { version: data.catalog_version || 'legacy', models: data.models });
      rememberProject({ auth_enabled: data.auth_enabled || false, user: data.user || null, projects: data.projects || [data.project] }, data.project.id, browserProjectStorage());
      setSession(old => ({ ...old, auth_enabled: data.auth_enabled ?? old?.auth_enabled ?? false, user: data.user ?? old?.user ?? null, account_security: data.account_security ?? old?.account_security, workspace: data.workspace, project: data.project, projects: data.projects }));
      setAuthState('ready');
      const oldPending = pendingByProject.current.get(data.user?.id || 'demo', data.project.id) || null;
      setPending(oldPending); if (oldPending) setError(unknownMessage);
      setSubmitting(inflight.current.has(data.project.id)); await refreshFor(token);
    } catch (cause) { failure(cause, token); }
    finally { if (current(token)) setLoading(false); }
  }, [clearProject, current, failure, refreshFor]);
  const connect = useCallback(async () => {
    const token = scope.current.transition(null); clearProject(); setLoading(true); setAuthError(''); setAuthIssue(null);
    try {
      let data: AuthSession;
      try { data = await api.session(); } catch (cause) { if (cause instanceof ApiError && cause.status === 404) data = { auth_enabled: false, user: null }; else throw cause; }
      if (!current(token)) return; setSession(data);
      if (data.auth_enabled && !data.user) { setAuthState('required'); setLoading(false); return; }
      setAuthState('ready'); await loadProject(readProjectPreference(data, browserProjectStorage()));
    } catch (cause) { if (current(token)) { setAuthError(cause instanceof Error ? cause.message : '无法恢复会话'); setAuthIssue(cause instanceof ApiError ? cause : null); setAuthState('checking'); setLoading(false); } }
  }, [clearProject, current, loadProject]);
  useEffect(() => { alive.current = true; void connect(); return () => { alive.current = false; scope.current.transition(null); closeStreams(); }; }, [connect, closeStreams]);
  const refreshIdentity = useCallback(async () => {
    const token = scope.current.capture(), owner = bootstrapRef.current?.user?.id;
    try { const value = await api.session(); if (!current(token)) return; if (value.auth_enabled && !value.user) { expireSession(); return; } if (owner !== value.user?.id) { await connect(); return; } setSession(value); if (bootstrapRef.current) { const next = { ...bootstrapRef.current, user: value.user, account_security: value.account_security }; bootstrapRef.current = next; setBootstrap(next); } }
    catch (cause) { failure(cause, token); }
  }, [current, expireSession, connect, failure]);
  const refreshCatalog = useCallback(async () => {
    const token = scope.current.capture(), data = bootstrapRef.current;
    if (!data || !token.projectId || !current(token) || catalogLock.current || Date.now() < catalogRetryAt.current || document.visibilityState === 'hidden') return;
    const identity = catalogScope(data.user?.id || 'demo', data.workspace.id), ticket = ++catalogSequence.current;
    catalogLock.current = true; setCatalogLoading(true); catalogAbort.current = new AbortController();
    try {
      const result = await api.modelCatalog(token.projectId, catalogCache.current.get(identity), catalogAbort.current.signal);
      if (!current(token) || ticket !== catalogSequence.current) return;
      if (!validCatalog(result.catalog)) throw new ApiError('模型目录格式不完整，继续保留上次有效目录。');
      catalogCache.current.set({ scope: identity, catalog: result.catalog, etag: result.etag });
      if (bootstrapRef.current && bootstrapRef.current.catalog_version !== result.catalog.version) { const next = { ...bootstrapRef.current, models: result.catalog.models, catalog_version: result.catalog.version }; bootstrapRef.current = next; setBootstrap(next); }
      setCatalogError(null); catalogRetryAt.current = 0; setCatalogUpdatedAt(Date.now());
    } catch (cause) { if (current(token) && ticket === catalogSequence.current) { if (cause instanceof ApiError && cause.status === 401) failure(cause, token); else { const issue = cause instanceof ApiError ? cause : new ApiError('模型目录刷新失败，现有选择已保留。'); setCatalogError(issue); catalogRetryAt.current = issue.retryAt; } } }
    finally { if (ticket === catalogSequence.current) { catalogLock.current = false; if (current(token)) setCatalogLoading(false); } }
  }, [current, failure]);
  useEffect(() => {
    if (!bootstrap) return;
    let timer: number | undefined;
    const update = () => { window.clearInterval(timer); if (document.visibilityState === 'hidden') { catalogSequence.current++; catalogAbort.current?.abort(); catalogLock.current = false; setCatalogLoading(false); return; } void refreshCatalog(); timer = window.setInterval(() => void refreshCatalog(), 60000); };
    update(); document.addEventListener('visibilitychange', update);
    return () => { window.clearInterval(timer); document.removeEventListener('visibilitychange', update); catalogSequence.current++; catalogAbort.current?.abort(); catalogLock.current = false; };
  }, [bootstrap?.project.id, bootstrap?.workspace.id, refreshCatalog]);
  const inspectSession = useCallback(async (token: ScopeToken) => {
    if (!bootstrapRef.current?.auth_enabled || Date.now() - lastSessionCheck.current < 3000) return;
    lastSessionCheck.current = Date.now();
    try { const data = await api.session(); if (current(token) && data.auth_enabled && !data.user) expireSession(); }
    catch (cause) { if (cause instanceof ApiError && cause.status === 401) failure(cause, token); }
  }, [current, expireSession, failure]);
  useEffect(() => {
    if (!bootstrap) return;
    const token = scope.current.capture(), projectId = bootstrap.project.id, owner = bootstrap.user?.id || 'demo';
    const source = new EventSource(`/api/v1/workspaces/${encodeURIComponent(bootstrap.workspace.id)}/events?project_id=${encodeURIComponent(projectId)}`);
    const abort = new AbortController(); streams.current.add(source); let closed = false, syncFailed = false;
    const active = () => !closed && current(token) && (bootstrapRef.current?.user?.id || 'demo') === owner;
    const sync = new DurableFeedSync(async batch => {
      if (!active()) return;
      const areas = batch.areas.filter(area => area !== 'history');
      if (areas.length) setUpdates(old => active() ? { activity: old.activity + Number(areas.includes('activity')), library: old.library + Number(areas.includes('library')), references: old.references + Number(areas.includes('references')), draft: old.draft + Number(areas.includes('draft')), conversations: old.conversations + Number(areas.includes('conversations')) } : old);
      if (batch.areas.includes('history')) {
        if (!syncFailed) setSyncNotice('正在补齐项目状态…');
        // Refresh recent history and every already-loaded nonterminal resource in bounded follow-up batches.
        const loadedTasks = tasksRef.current, loadedRuns = runsRef.current, currentRun = selectedRunRef.current;
        function* loadedResources(): Generator<ResourceHint> {
          for (const task of loadedTasks) if (!terminal(task.status)) yield { type: 'task', id: task.id, version: task.version, status: task.status };
          for (const run of loadedRuns) if (!terminal(run.status) || run.id === currentRun) yield { type: 'run', id: run.id, version: run.seq, status: run.status };
        }
        sync.beginReconciliation(batch.resetId!, loadedResources());
        await refreshFor(token); if (!active()) return;
      }
      await boundedReads(batch.resources, async hint => {
        if (!active()) return;
        try {
          if (hint.type === 'task') { const task = await api.task(hint.id, projectId, abort.signal); if (!active()) return; if (!scope.current.accepts(token, task) || task.id !== hint.id) throw new ApiError('任务快照身份不匹配。'); if (!resourceHintCovered({ type: 'task', id: task.id, version: task.version, status: task.status }, hint)) throw new ApiError('任务快照尚未覆盖通知版本，正在核对。'); setTasks(old => { if (!active()) return old; const next = mergeTask(old, task); tasksRef.current = next; return next; }); sync.acknowledge({ type: 'task', id: task.id, version: task.version, status: task.status }); }
          else { const run = await api.snapshot(hint.id, projectId, abort.signal); if (!active()) return; if (!scope.current.accepts(token, run) || run.id !== hint.id) throw new ApiError('对话快照身份不匹配。'); if (!resourceHintCovered({ type: 'run', id: run.id, version: run.seq, status: run.status }, hint)) throw new ApiError('对话快照尚未覆盖通知版本，正在核对。'); updateRun(run, token); }
        } catch (cause) { if (cause instanceof ApiError && cause.status === 404) return; throw cause; }
      });
      if (active()) { syncFailed = false; setSyncNotice(''); }
    }, (cause, paused) => { if (!active()) return; syncFailed = true; if (cause instanceof ApiError && cause.status === 401) failure(cause, token); else setSyncNotice(paused ? '项目同步暂未完成，现有内容与未确认操作已保留。请点击“恢复同步”。' : '正在重试读取项目状态，未确认操作与编辑保持不变。'); });
    feedSync.current = sync;
    const visibility = () => sync.setVisible(document.visibilityState !== 'hidden'); visibility(); document.addEventListener('visibilitychange', visibility);
    source.onopen = () => { if (active()) { setTaskConnection('实时已连接'); sync.resume(); } };
    source.onerror = () => { if (active()) { setTaskConnection(source.readyState === EventSource.CLOSED ? '连接已关闭，请恢复' : '连接恢复中'); void inspectSession(token); } };
    source.addEventListener('stream.reset', ((event: MessageEvent) => { if (!active()) return; if (validStreamReset(event.data)) sync.reset(); else setSyncNotice('同步通知格式异常，请手动恢复同步。'); }) as EventListener);
    source.addEventListener('resource.changed', ((event: MessageEvent) => { if (!active()) return; const value = parseResourceChanged(event.data); if (value) sync.accept(value); else setSyncNotice('同步通知格式异常，请手动恢复同步。'); }) as EventListener);
    source.addEventListener('task.updated', ((event: MessageEvent) => {
      if (!active()) return;
      try { const body = JSON.parse(event.data) as { task: Task }; if (body.task?.id && Number.isSafeInteger(body.task.version) && scope.current.accepts(token, body.task)) { sync.acknowledge({ type: 'task', id: body.task.id, version: body.task.version, status: body.task.status }); setTasks(old => { if (!active()) return old; const next = mergeTask(old, body.task); tasksRef.current = next; return next; }); } }
      catch { setTaskConnection('事件异常，请刷新'); }
    }) as EventListener);
    return () => { closed = true; document.removeEventListener('visibilitychange', visibility); abort.abort(); sync.stop(); if (feedSync.current === sync) feedSync.current = null; source.close(); streams.current.delete(source); };
  }, [bootstrap?.project.id, bootstrap?.workspace.id, bootstrap?.user?.id, reconnect, current, failure, inspectSession, refreshFor, updateRun]);
  useEffect(() => {
    if (!selectedRun || !bootstrap) { setRunConnection(''); return; }
    const token = scope.current.capture(), projectId = bootstrap.project.id;
    let closed = false, recovering = false;
    let source: EventSource | null = null, batchTimer: number | undefined, local: Run | undefined;
    const active = () => !closed && current(token);
    const closeSource = () => { if (source) { source.close(); streams.current.delete(source); source = null; } };
    const flush = () => { window.clearTimeout(batchTimer); batchTimer = undefined; if (active() && local) updateRun(local, token); };
    const schedule = () => { if (batchTimer === undefined) batchTimer = window.setTimeout(flush, 40); };
    const start = async () => {
      if (recovering || !active()) return; recovering = true; closeSource(); setRunConnection('恢复快照');
      try {
        const snapshot = await api.snapshot(selectedRun, projectId);
        if (!active() || !scope.current.accepts(token, snapshot)) return;
        local = snapshot; updateRun(snapshot, token); if (terminal(snapshot.status)) { setRunConnection(''); return; }
        source = new EventSource(`/api/v1/runs/${encodeURIComponent(selectedRun)}/events?after=${snapshot.seq}&project_id=${encodeURIComponent(projectId)}`); streams.current.add(source);
        source.onopen = () => { if (active()) setRunConnection(local?.moderation ? '生成与检查中' : '实时输出'); };
        source.onerror = () => { if (active()) { setRunConnection(local?.moderation ? '连接恢复中，正在核对检查状态' : '连接恢复中，输出已保留'); void inspectSession(token); } };
        for (const type of ['message.delta', 'run.snapshot', 'run.completed', 'run.cancelled', 'run.failed', 'run.interrupted']) source.addEventListener(type, ((event: MessageEvent) => {
          if (!active() || !local) return;
          try {
            const result = applyRunEvent(local, { event: type, id: event.lastEventId, data: JSON.parse(event.data) });
            if (result.gap) { closeSource(); void start(); return; }
            if (!scope.current.accepts(token, result.run)) return; local = result.run;
            if (terminal(local.status)) { flush(); closeSource(); setRunConnection(''); void api.snapshot(selectedRun, projectId).then(value => { if (active()) updateRun(value, token); }).catch(cause => { if (active()) failure(cause, token); }); }
            else schedule();
          } catch { closeSource(); setRunConnection('事件格式异常，请恢复连接'); }
        }) as EventListener);
      } catch (cause) { if (active()) { setRunConnection('连接中断，可恢复'); failure(cause, token); } }
      finally { recovering = false; }
    };
    void start(); return () => { closed = true; closeSource(); window.clearTimeout(batchTimer); };
  }, [bootstrap?.project.id, bootstrap?.workspace.id, selectedRun, reconnect, current, failure, inspectSession, updateRun]);
  const submitPending = useCallback(async (operation: Pending) => {
    const token = scope.current.capture(), projectId = operation.body.project_id;
    if (!current(token) || projectId !== token.projectId || operation.ownerId !== (bootstrapRef.current?.user?.id || 'demo') || inflight.current.has(projectId)) return false;
    const priorUnknown = pendingByProject.current.get(operation.ownerId, projectId)?.key === operation.key;
    // A session can expire while this POST is still in flight. Keep its identity before sending.
    pendingByProject.current.remember(operation);
    const flight = inflight.current.claim(projectId, operation.key, token.revision); setSubmitting(true); setError('');
    try {
      if (operation.kind === 'task') { const task = await api.createTask(operation.body, operation.key); if (scope.current.accepts(token, task)) setTasks(old => current(token) ? mergeTask(old, task) : old); }
      else { const run = await api.createRun(operation.body, operation.key); if (scope.current.accepts(token, run)) { updateRun(run, token); setSelectedRun(run.id); } }
      pendingByProject.current.acknowledge(operation);
      if (current(token)) { setPending(null); setSubmissionRetryAt(0); } return current(token);
    } catch (cause) {
      const uncertain = keepUnknown(priorUnknown, cause instanceof ApiError ? cause : {});
      pendingByProject.current.failed(operation, uncertain, current(token));
      if (!current(token)) return false;
      if (cause instanceof ApiError && cause.status === 401) { failure(cause, token); return false; }
      if (current(token)) { setPending(uncertain ? operation : null); setErrorText((cause instanceof Error ? cause.message : '提交失败') + (uncertain ? ' ' + unknownMessage : '')); setRequestError(cause instanceof ApiError ? cause : null); setSubmissionRetryAt(cause instanceof ApiError ? cause.retryAt : 0); }
      return false;
    } finally {
      inflight.current.release(projectId, flight);
      if (current(token)) setSubmitting(inflight.current.has(projectId));
    }
  }, [current, failure, updateRun]);
  const authenticate = async (mode: 'login' | 'register', body: { email: string; password: string; name: string }) => {
    if (authLock.current || Date.now() < (authIssue?.retryAt || 0)) return; authLock.current = true; setAuthBusy(true); setAuthError(''); setAuthIssue(null);
    const token = scope.current.transition(null); clearProject();
    try { const result = mode === 'register' ? await api.register(body) : await api.login({ email: body.email, password: body.password }); if (!current(token)) return; setSession(result); setAuthState('ready'); await loadProject(readProjectPreference(result, browserProjectStorage()) || result.project?.id); }
    catch (cause) { if (current(token)) { setAuthError(cause instanceof Error ? cause.message : '登录失败'); setAuthIssue(cause instanceof ApiError ? cause : null); setAuthState('required'); setLoading(false); } }
    finally { authLock.current = false; if (alive.current) setAuthBusy(false); }
  };
  const logout = async () => {
    if (authLock.current) return; authLock.current = true; setAuthBusy(true);
    try { await api.logout(); if (alive.current) expireSession('你已安全退出。'); } catch (cause) { failure(cause); }
    finally { authLock.current = false; if (alive.current) setAuthBusy(false); }
  };
  const createProject = async (name: string) => {
    const token = scope.current.capture();
    try { const project = await api.createProject(name); if (!current(token)) return false; await loadProject(project.id); return true; } catch (cause) { failure(cause, token); return false; }
  };
  const create = async (kind: 'chat' | 'image' | 'video', selection: ModelSelection, prompt: string, operation: ImageOperation = 'generate', referenceIds: string[] = [], quoted?: QuotedRequest) => {
    const data = bootstrapRef.current; if (!data || pending || Date.now() < submissionRetryAt) return false;
    if (kind !== 'chat' && moderationMediaBlocked(data.moderation)) { setError(moderationErrorMessage('MODERATION_MEDIA_UNSUPPORTED')!); return false; }
    const model = data.models.find(item => item.kind === kind && item.id === selection.modelId), declared = modelCapability(data, model);
    if (!model || !declared.enabled) { setError(declared.reason || '所选模型已停用，请选择其他模型。'); return false; }
    if (model.version !== selection.modelVersion) { setError('模型目录刚刚更新，请确认新的模型规格后再提交。'); return false; }
    if (promptBytes(prompt) > declared.max_prompt_bytes) { setError('描述超过当前模型的文本长度限制，请缩短后提交。'); return false; }
    const preset = declared.parameter_presets.find(item => item.id === selection.presetId);
    if (kind !== 'chat' && (!preset || !Number.isInteger(selection.count) || selection.count < 1 || selection.count > declared.max_count || kind === 'video' && selection.count !== 1)) { setError('所选规格已不适用，请刷新模型目录并重新选择。'); return false; }
    if (operation === 'edit' && (kind !== 'image' || !declared.image_edit_supported || referenceIds.length < 1 || referenceIds.length > declared.max_reference_images || new Set(referenceIds).size !== referenceIds.length)) { setError('请使用支持图片编辑的模型，并选择符合数量要求的已上传参考图。'); return false; }
    const request: QuoteRequest = kind === 'chat' ? { resource_type: 'run', run_input: { project_id: data.project.id, model_id: model.id, ...(model.version ? { model_version: model.version } : {}), input: prompt } } : { resource_type: 'task', task_input: buildModelTaskInput(data.project.id, model, prompt, preset!, selection.count, operation, referenceIds) };
    if (billingEnabled(data) && !quoteMatches(quoted, billingScope(data), request)) { setError('请为当前输入重新获取并确认报价。原待确认请求请使用原请求重试。'); return false; }
    const quoteId = billingEnabled(data) ? quoted!.quote.id : undefined;
    const key = crypto.randomUUID(), ownerId = data.user?.id || 'demo';
    return submitPending(request.resource_type === 'run' ? { kind: 'run', ownerId, key, body: { ...request.run_input, ...(quoteId ? { quote_id: quoteId } : {}) } } : { kind: 'task', ownerId, key, body: { ...(request as Extract<QuoteRequest, { resource_type: 'task' }>).task_input, ...(quoteId ? { quote_id: quoteId } : {}) } });
  };
  const cancel = async (kind: 'run' | 'task', id: string) => {
    const token = scope.current.capture(); if (!token.projectId) return;
    try {
      if (kind === 'run') { const value = await api.cancelRun(id, token.projectId); updateRun(value, token); if (current(token)) setReconnect(n => n + 1); }
      else { const value = await api.cancelTask(id, token.projectId); if (scope.current.accepts(token, value)) setTasks(old => current(token) ? mergeTask(old, value) : old); }
    } catch (cause) { failure(cause, token); }
  };
  const loadMore = async (kind: 'tasks' | 'runs') => {
    const token = scope.current.capture(), offset = historyOffsets[kind];
    if (!token.projectId || offset === null || historyLocks.current.has(kind)) return;
    historyLocks.current.set(kind, token.revision); historyExtended.current.add(kind); setHistoryLoading(old => ({ ...old, [kind]: true }));
    try {
      if (kind === 'tasks') {
        const page = await api.tasks(token.projectId, offset); if (!current(token)) return;
        const incoming = page.items.filter(task => scope.current.accepts(token, task)); setTasks(old => current(token) ? incoming.reduce(mergeTask, old) : old);
        setHistoryOffsets(old => ({ ...old, tasks: page.has_more ? offset + page.items.length : null }));
      } else {
        const page = await api.runs(token.projectId, offset); if (!current(token)) return;
        const next = page.items.filter(run => scope.current.accepts(token, run)).reduce(mergeRun, runsRef.current); runsRef.current = next; setRuns(next);
        setHistoryOffsets(old => ({ ...old, runs: page.has_more ? offset + page.items.length : null }));
      }
    } catch (cause) { failure(cause, token); }
    finally { if (historyLocks.current.get(kind) === token.revision) historyLocks.current.delete(kind); if (current(token)) setHistoryLoading(old => ({ ...old, [kind]: false })); }
  };
  const reconcileTask = async (id: string) => {
    const token = scope.current.capture(); if (!token.projectId) return;
    try { const value = await api.reconcileTask(id, token.projectId); if (scope.current.accepts(token, value)) setTasks(old => current(token) ? mergeTask(old, value) : old); }
    catch (cause) { failure(cause, token); }
  };
  return { bootstrap, session, authState, authBusy, authError, authIssue, authenticate, logout, expireSession, refreshIdentity, projects, createProject, switchProject: loadProject,
    updates, syncNotice, historyOffsets, historyLoading, loadMore, catalogLoading, catalogError, catalogUpdatedAt, refreshCatalog,
    receiveRun: (run: Run) => updateRun(run, scope.current.capture()), tasks, runs, selectedRun, setSelectedRun, loading, submitting, error, setError, requestError, submissionRetryAt, pending, taskConnection, runConnection,
    connect, create, cancel, reconcileTask, retry: () => pending && Date.now() >= submissionRetryAt && submitPending(pending),
    refresh: async () => { feedSync.current?.resume(); const token = scope.current.capture(); try { await refreshFor(token); if (current(token) && !pending) setError(''); } catch (cause) { failure(cause, token); } },
    recover: () => { feedSync.current?.resume(); setReconnect(n => n + 1); },
  };
}
