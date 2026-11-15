import { BillingPage } from './BillingPage';
import { BillingStatus, QuoteConfirmation } from './BillingComponents';
import { billingEnabled } from './billing';
import type { QuoteRequest, QuotedRequest } from './billing';
import { useBillingQuote } from './useBillingQuote';
import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { Artwork, DraftCanvas, Icon } from './components';
import type { IconName } from './components';
import { api, safeAssetURL } from './api';
import { LazyPage } from './LazyPage';
import { GenerationRecovery } from './generationControls';
import { GenerationBanner } from './GenerationStatus';
import { useGenerationAccess } from './useGenerationAccess';
const loadOperations = () => import('./OperationsPage').then(m => ({ default: m.OperationsPage }));
const loadGeneration = () => import('./GenerationAccessPage').then(m => ({ default: m.GenerationAccessPage }));
import { OperationsRecovery, operationScopes } from './operations';
import type { OperationAccess } from './operations';
import { useStudio } from './useStudio';
import { useCreationDraft } from './useCreationDraft';
import { ChatWorkspace } from './ChatWorkspace';
import { ModerationDelivery, ModerationStatus } from './ModerationStatus';
import { MediaModerationNotice, MediaModerationStatus } from './MediaModerationStatus';
import { canQueryModeratedTask, mediaModerationErrorMessage, taskMediaAllowed } from './mediaModeration';
import { moderationAllowsText, moderationErrorMessage, moderationMediaBlocked, moderationState, moderationText, visibleModeratedText } from './moderation';
import { ConversationRecovery } from './conversationState';
import type { Artifact, Kind, Task, View } from './types';
import { statusName, terminal } from './types';
import { MAX_PROMPT_UNITS } from './submissions';
import { canReconcile, isDemoResource, reconciliationStates } from './capabilities';
import { AuthGate } from './AuthGate';
import { AccountRecovery } from './AccountRecovery';
import { AccountSecurityPage } from './AccountSecurityPage';
import { takeAccountLink } from './accountSecurity';
import type { AccountLink } from './accountSecurity';
import { CloudDraftCanvas } from './CloudDraftCanvas';
import { ActivityPage } from './ActivityPage';
import { DraftRecoveryStore } from './draftRecovery';
import { RuntimePanel } from './RuntimePanel';
import { RequestDetails } from './RequestDetails';
import { useCountdown } from './useCountdown';
import { ModelControls } from './ModelControls';
import { ReferencePicker } from './ReferencePicker';
import { useReferences } from './useReferences';
import { LibraryPage } from './LibraryPage';
import { LibraryRecovery } from './libraryState';
import { buildModelTaskInput, canCancelResource, historicalModelName, modelCapability, presetSummary, promptBytes, resolveSelection } from './modelCatalog';
import type { ModelSelection } from './modelCatalog';

const navigation: { id: View; icon: IconName; name: string; label: string }[] = [
  { id: 'chat', icon: 'chat', name: '灵感对话', label: '01' },
  { id: 'image', icon: 'image', name: '图像工作室', label: '02' },
  { id: 'video', icon: 'video', name: '视频工作室', label: '03' },
  { id: 'canvas', icon: 'layers', name: '创作草稿', label: 'LOCAL' },
  { id: 'assets', icon: 'grid', name: '作品素材', label: '' },
  { id: 'tasks', icon: 'folder', name: '任务中心', label: '' },
  { id: 'activity', icon: 'folder', name: '活动记录', label: 'LOG' },
  { id: 'billing', icon: 'layers', name: '工作区权益', label: 'CREDIT' },
  { id: 'security', icon: 'sliders', name: '账户安全', label: 'ACCOUNT' },
  { id: 'generation', icon: 'sliders', name: '生成权限与复核', label: 'ACCESS' },
  { id: 'operations', icon: 'sliders', name: '运营核实', label: 'OPS' },
];
const templates = [
  { title: '森林，写成一封情书。', tag: 'BRAND / STILL LIFE', variant: 'forest' as const, prompt: '为独立香氛品牌创作一张产品海报：苔藓森林中的磨砂玻璃香水瓶，柔和晨光穿透树冠，墨绿与暖米色调，克制的高级感，留出品牌文字空间。' },
  { title: '月亮之外，还有远方。', tag: 'CINEMA / LANDSCAPE', variant: 'lunar' as const, prompt: '月光照亮寂静山谷，一条银色河流蜿蜒至远方，蓝灰色山脊层层延伸，镜头缓慢向前推进，薄雾流动，安静、诗意、电影感。' },
  { title: '光，是空间的旁白。', tag: 'DESIGN / ARCHITECTURE', variant: 'arc' as const, prompt: '极简建筑空间，沙色拱门、几何体块与一轮朱红色太阳，午后光线投下清晰而悠长的阴影，精密构图，温暖的侘寂质感。' },
];
const headings: Record<Kind, { eyebrow: string; first: string; last: string; description: string; placeholder: string }> = {
  chat: { eyebrow: 'THINK BEYOND THE OBVIOUS', first: '给想法，', last: '一个新的可能。', description: '理清思路，打磨文字，让下一步创作从这里开始。', placeholder: '说说你的想法。一次提案、一段故事，或者一个还没成形的灵感…' },
  image: { eyebrow: 'IMAGINATION, MADE VISIBLE', first: '让想象，', last: '拥有形状。', description: '从一句描述，到一幅有你风格的画面。', placeholder: '描述你想看见的画面：主体、环境、光线、风格，以及那些与众不同的细节…' },
  video: { eyebrow: 'EVERY FRAME TELLS A STORY', first: '让画面，', last: '开始叙事。', description: '将构图、运动与时间，编织成一段有情绪的镜头。', placeholder: '描述镜头的主体、动作、运镜和氛围，例如：月下山谷，镜头缓慢推进，薄雾轻轻流动…' },
};

export function ArtifactPreview({ artifact, demo, allowed = true, sample = false }: { artifact: Artifact; demo: boolean; allowed?: boolean; sample?: boolean }) {
  const [failed, setFailed] = useState(false);
  const url = allowed ? safeAssetURL(artifact.url) : undefined;
  if (!allowed) return null;
  if (!url || failed) return <div className="asset-fallback"><Icon name="image" size={28}/><span>预览暂不可用</span></div>;
  return <div className="artifact-preview">{artifact.kind === 'video' && !url.split('?')[0].endsWith('.svg')
    ? <video src={url} controls preload="none" playsInline aria-label={artifact.title}/>
    : <img src={url} alt={artifact.title || '生成任务结果'} loading="lazy" decoding="async" onError={() => setFailed(true)}/>}
    <span className="image-caption">{demo ? sample ? 'DEMO · 本地审核样本，非 AI 生成' : 'DEMO PROVIDER · 示意结果' : '生成结果'}</span>
    <a className="asset-open" href={url} target="_blank" rel="noreferrer" aria-label={'打开结果 ' + artifact.title}><Icon name="external" size={15}/></a>
  </div>;
}

export function TaskCard({ task, demo, canCancel, onCancel, onReconcile, onRefresh }: { task: Task; demo: boolean; canCancel: boolean; onCancel: (id: string) => Promise<void>; onReconcile: (id: string) => Promise<void>; onRefresh: (id: string) => Promise<void> }) {
  const [cancelling, setCancelling] = useState(false);
  const [reconciling, setReconciling] = useState(false);
  const taskDemo = isDemoResource(task, demo ? 'demo' : 'live');
  const progress = Math.max(0, Math.min(100, task.progress)), allowed = taskMediaAllowed(task), reviewState = moderationState(task.moderation);
  const reviewing = reviewState === 'BLOCKED' || reviewState === 'REVIEW', originalQuery = canQueryModeratedTask(task);
  return <article className="task-card"><div className="task-card-header"><span className="task-kind"><Icon name={task.kind}/>{task.kind === 'image' ? task.operation === 'edit' ? '图片编辑' : '图像生成' : '视频生成'}<small className="task-provider">{taskDemo ? 'DEMO' : 'LIVE'}</small></span><span className={'state-pill state-' + task.status.toLowerCase()}>{statusName(task.status)}</span></div><p className="task-prompt">{task.prompt}</p><p className="task-model-identity">{task.operation === 'edit' && <small>{task.reference_asset_ids?.length || 0} 张参考图</small>}<span>{historicalModelName(task)}</span>{task.model_version && <small title={task.model_version} aria-label={'受理时模型规格版本：' + task.model_version}>受理时规格</small>}</p><MediaModerationStatus value={task.moderation} status={task.status} canQueryOriginal={originalQuery} onRefresh={() => void onRefresh(task.id)}/>{allowed && task.artifacts?.length > 0 && <div className="task-artifacts">{task.artifacts.map(artifact => <ArtifactPreview key={artifact.id} artifact={artifact} demo={taskDemo} allowed={allowed} sample={!!task.moderation && taskDemo}/>)}</div>}{!terminal(task.status) && !reviewing && <div className="progress-track" role="progressbar" aria-label="任务进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress}><i style={{ width: progress + '%' }}/></div>}<BillingStatus value={task.billing}/>{task.error && allowed && <p className="inline-error">{mediaModerationErrorMessage(task.error.code) || task.error.message}</p>}{!task.moderation && reconciliationStates.includes(task.status) && <p className="task-advisory">{task.status === 'SUBMIT_UNKNOWN' ? '上游受理结果待核实。' : '任务需要核对，可能已有上游结果。'}平台不会自动重新提交，请保留此任务等待核对。</p>}{!terminal(task.status) && !canCancel && <p className="task-advisory">该服务不支持撤回已提交任务；离开页面不会取消生成。</p>}<div className="task-card-footer">{(originalQuery || !task.moderation && canReconcile(task)) && <div className="reconcile-action"><span>仅查询原任务，不会重新生成。</span><button className="button secondary" disabled={reconciling} onClick={async () => { setReconciling(true); try { await onReconcile(task.id); } finally { setReconciling(false); } }}><Icon name="refresh" size={13}/>{reconciling ? '正在请求核对…' : originalQuery ? '查询原任务' : '重新核对'}</button></div>}<span title={task.id}>{task.id.slice(0, 16)}… <span className="task-version">v{task.version}</span></span>{!terminal(task.status) && !reviewing && canCancel && !reconciliationStates.includes(task.status) && <button className="text-button danger" disabled={cancelling} onClick={async () => { setCancelling(true); try { await onCancel(task.id); } finally { setCancelling(false); } }}>{cancelling ? '正在请求…' : '取消任务'}</button>}{terminal(task.status) && <time>{new Date(task.created_at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}</time>}</div></article>;
}

export default function App({ initialAccountLink = null }: { initialAccountLink?: AccountLink | null }) {
  const studio = useStudio();
  const submissionCooldown = useCountdown(studio.submissionRetryAt);
  const [view, setView] = useState<View>('image');
  const [accountLink, setAccountLink] = useState<AccountLink | null>(initialAccountLink), [recoveryOpen, setRecoveryOpen] = useState(!!initialAccountLink), [recoveryVersion, setRecoveryVersion] = useState(0);
  const [securityOpened, setSecurityOpened] = useState(false), [securityBusy, setSecurityBusy] = useState(false);
  const [operationsAccess, setOperationsAccess] = useState<OperationAccess | null>(null), [operationsAccessVersion, setOperationsAccessVersion] = useState(0), [operationsBusy, setOperationsBusy] = useState(false);
  const generationRecovery = useRef(new GenerationRecovery());
  const operationsRecovery = useRef(new OperationsRecovery()), operationsLeaveRef = useRef<HTMLDialogElement>(null);
  const [operationsLeave, setOperationsLeave] = useState<(() => void) | null>(null);
  const operationsOwner = studio.session?.user?.id || '';
  useEffect(() => { let active = true; const controller = new AbortController(); setOperationsAccess(null); if (studio.authState === 'ready' && operationsOwner) void api.operationsAccess(controller.signal).then(value => { if (active) setOperationsAccess(value); }).catch(() => { if (active) setOperationsAccess(null); }); return () => { active = false; controller.abort(); }; }, [operationsOwner, studio.authState, operationsAccessVersion]);
  useEffect(() => { if (operationsLeave) operationsLeaveRef.current?.showModal(); else operationsLeaveRef.current?.close(); }, [operationsLeave]);
  useEffect(() => { setOperationsLeave(null); setOperationsBusy(false); }, [operationsOwner]);
  const accountLinkRef = useRef(accountLink); accountLinkRef.current = accountLink;
  useEffect(() => { const receive = () => { const value = takeAccountLink(window.location, window.history); if (!value) return; const old = accountLinkRef.current; if (old && old.kind !== 'invalid') old.secret.clear(); accountLinkRef.current = value; setAccountLink(value); setRecoveryVersion(version => version + 1); setRecoveryOpen(true); }; window.addEventListener('hashchange', receive); return () => window.removeEventListener('hashchange', receive); }, []);
  const { prompt, setPrompt, imageOperation, setImageOperation } = useCreationDraft();
  const [preferences, setPreferences] = useState<Partial<Record<Kind, ModelSelection>>>({});
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [inspectorOpen, setInspectorOpen] = useState(() => window.innerWidth > 1180);
  const [notice, setNotice] = useState('');
  const [taskFilter, setTaskFilter] = useState<'all' | 'active' | 'complete'>('all');
  const [projectForm, setProjectForm] = useState(false);
  const [projectName, setProjectName] = useState('');
  const [projectSaving, setProjectSaving] = useState(false);
  const [cloudDirty, setCloudDirty] = useState(false);
  const [draftOpened, setDraftOpened] = useState(false);
  const [pendingLeave, setPendingLeave] = useState<(() => void) | null>(null);
  const leaveDialogRef = useRef<HTMLDialogElement>(null);
  const draftRecovery = useRef(new DraftRecoveryStore());
  const libraryRecovery = useRef(new LibraryRecovery());
  const conversationRecovery = useRef(new ConversationRecovery());
  const [chatOpened, setChatOpened] = useState(false), [newChatVersion, setNewChatVersion] = useState(0);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const mainRef = useRef<HTMLElement>(null);
  const isCreator = view === 'chat' || view === 'image' || view === 'video';
  const kind: Kind = isCreator ? view : 'image';
  const operation = kind === 'image' ? imageOperation : 'generate';
  const resolved = resolveSelection(studio.bootstrap, kind, preferences[kind], operation);
  const { model, preset, presets, selection } = resolved;
  const legacyRuns = studio.runs.filter(run => !run.conversation_id);
  const selectedRun = legacyRuns.find(run => run.id === studio.selectedRun);
  const activeRun = selectedRun && !terminal(selectedRun.status);
  const selectedRunVisible = !selectedRun || moderationAllowsText(selectedRun.moderation, selectedRun.status);
  const selectedRunText = selectedRun ? visibleModeratedText({ ...selectedRun, text: selectedRun.output }) : ''; 
  const activeTasks = studio.tasks.filter(task => !terminal(task.status));
  const demo = studio.bootstrap?.mode !== 'live';
  const selectedRunDemo = selectedRun ? isDemoResource(selectedRun, demo ? 'demo' : 'live') : demo;
  const currentCapability = resolved.capabilities;
  const mediaModerationBlocked = kind !== 'chat' && moderationMediaBlocked(studio.bootstrap?.moderation);
  const mediaUnavailable = !currentCapability.enabled || mediaModerationBlocked;
  const references = useReferences({ ownerId: studio.session?.user?.id || 'demo', projectId: studio.bootstrap?.project.id, limits: studio.bootstrap?.reference_limits, maxSelected: currentCapability.max_reference_images, refreshVersion: studio.updates.references, visible: view === 'image' && operation === 'edit', onAuthExpired: studio.expireSession });
  const editInvalid = operation === 'edit' && (!references.referenceIds?.length || references.referenceIds.length > currentCapability.max_reference_images);
  const effectiveCount = selection.count;
  const promptLength = promptBytes(prompt);
  const promptTooLong = promptLength > currentCapability.max_prompt_bytes;
  const generationRequired = studio.bootstrap?.storage === 'postgres' && studio.bootstrap.auth_enabled === true && !!operationsOwner;
  const generationAccess = useGenerationAccess({ owner: operationsOwner, workspace: studio.bootstrap?.workspace.id || '', required: generationRequired, visible: studio.authState === 'ready' && (isCreator || view === 'generation'), onExpired: studio.expireSession });
  const creationInputReady = generationAccess.allowed && !!studio.bootstrap && !!model && !!prompt.trim() && prompt.length <= MAX_PROMPT_UNITS && !mediaUnavailable && !promptTooLong && !editInvalid && (kind === 'chat' || !!preset);
  // A partially edited form is normal UI state. Keep strict request construction behind the same readiness gate as submission.
  const quoteRequest: QuoteRequest | null = creationInputReady && studio.bootstrap && model && preset && (view === 'image' || view === 'video') ? { resource_type: 'task', task_input: buildModelTaskInput(studio.bootstrap.project.id, model, prompt.trim(), preset, selection.count, operation, operation === 'edit' ? references.referenceIds || [] : []) } : null;
  const quote = useBillingQuote(studio.bootstrap, quoteRequest, studio.expireSession);
  const title = navigation.find(item => item.id === view)?.name;

  function navigate(next: View) { if (securityBusy || operationsBusy) { setNotice('敏感操作处理中，请稍候再离开。'); return; } const change = () => { if (next === 'security') setSecurityOpened(true); if (next === 'chat') setChatOpened(true); if (next === 'canvas') setDraftOpened(true); setView(next); setSidebarOpen(false); mainRef.current?.scrollTo({ top: 0 }); }; if (view === 'operations' && next !== view && (operationsRecovery.current.hasFor(operationsOwner) || generationRecovery.current.hasOpsFor(operationsOwner))) setOperationsLeave(() => change); else change(); }
  function selectModel(id: string) {
    const next = studio.bootstrap?.models.find(item => item.id === id); if (!next) return;
    const cap = modelCapability(studio.bootstrap, next); if (!cap.enabled) { setNotice(cap.reason || '该模型暂未开放。'); return; }
    if (next.kind === 'image' && imageOperation === 'edit' && !cap.image_edit_supported) { setNotice('该模型尚未开放参考图编辑，请先切换为文字生成或选择支持编辑的模型。'); return; }
    const previous = resolveSelection(studio.bootstrap, next.kind, preferences[next.kind], next.kind === 'image' ? imageOperation : 'generate');
    if (previous.model?.id === next.id) { navigate(next.kind); return; }
    const nextPreset = cap.parameter_presets[0], nextCount = next.kind === 'video' ? 1 : Math.min(previous.count, cap.max_count);
    setPreferences(old => ({ ...old, [next.kind]: { modelId: next.id, modelVersion: next.version, presetId: nextPreset?.id, count: nextCount } }));
    if (presetSummary(previous.preset) !== presetSummary(nextPreset) || nextCount !== previous.count) setNotice(`已切换模型，${nextCount !== previous.count ? '规格／数量' : '规格'}已调整，请核对后提交。`);
    navigate(next.kind);
  }
  useEffect(() => { if (resolved.adjusted && studio.bootstrap) { setPreferences(old => ({ ...old, [kind]: selection })); setNotice('模型或规格已更新，不兼容的选择已调整。请核对参数后提交；原待确认请求保持不变。'); } }, [resolved.adjusted, kind, selection.modelId, selection.modelVersion, selection.presetId, selection.count, studio.bootstrap?.catalog_version]);
  useEffect(() => { setPreferences({}); }, [studio.session?.user?.id, studio.bootstrap?.workspace.id]);
  function guardProjectLeave(action: () => void) { if (securityBusy || operationsBusy) { setNotice('敏感操作处理中，请稍候再切换。'); return; } const proceed = () => { const key = 'xingtu-local-draft-v2:' + (studio.session?.user?.id || 'demo') + ':' + studio.bootstrap?.project.id; if (cloudDirty || draftRecovery.current.has(key) || references.currentPending) setPendingLeave(() => action); else action(); }; if (operationsRecovery.current.hasFor(operationsOwner) || generationRecovery.current.hasFor(operationsOwner)) setOperationsLeave(() => proceed); else proceed(); }
  useEffect(() => { if (!notice) return; const id = window.setTimeout(() => setNotice(''), 4500); return () => window.clearTimeout(id); }, [notice]);
  useEffect(() => { setPrompt(''); setImageOperation('generate'); setNotice(''); setProjectForm(false); setProjectName(''); setCloudDirty(false); setDraftOpened(false); setPendingLeave(null); }, [studio.bootstrap?.project.id, studio.session?.user?.id]);
  useEffect(() => { if (pendingLeave) leaveDialogRef.current?.showModal(); else leaveDialogRef.current?.close(); }, [pendingLeave]);
  useEffect(() => { const guard = (event: BeforeUnloadEvent) => { if (draftRecovery.current.hasUnsaved() || libraryRecovery.current.hasUnsaved() || conversationRecovery.current.hasUnsaved() || operationsRecovery.current.has() || generationRecovery.current.has()) { event.preventDefault(); event.returnValue = ''; } }; window.addEventListener('beforeunload', guard); return () => window.removeEventListener('beforeunload', guard); }, []);
  useEffect(() => { const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') { setSidebarOpen(false); if (window.innerWidth <= 1180) setInspectorOpen(false); } }; document.addEventListener('keydown', escape); return () => document.removeEventListener('keydown', escape); }, []);

  async function submit(event?: FormEvent, quoted?: QuotedRequest) {
    event?.preventDefault();
    if (!isCreator || !creationInputReady || studio.submitting || studio.pending || submissionCooldown > 0) return;
    if (prompt.length > MAX_PROMPT_UNITS) { studio.setError('创作描述最多 5,000 字符，请缩短后提交。'); return; }
    if (billingEnabled(studio.bootstrap) && !quoted) { void quote.fetchQuote(); return; }
    const sentPrompt = prompt;
    const accepted = await studio.create(kind, selection, prompt.trim(), operation, operation === 'edit' ? references.referenceIds || [] : [], quoted);
    quote.clear();
    if (accepted) { setPrompt(value => value === sentPrompt ? '' : value); setNotice(kind === 'chat' ? studio.bootstrap?.moderation?.enabled ? '对话已受理，完整生成并通过检查后展示。' : '对话已受理，正在接收实时输出。' : '任务已受理，切换页面后仍可在任务中心查看进度。'); }
  }

  function applyTemplate(index: number) { setPrompt(templates[index].prompt); if (!isCreator) setView('image'); window.setTimeout(() => { textareaRef.current?.focus(); textareaRef.current?.scrollIntoView({ block: 'center', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' }); }, 0); }
  const filteredTasks = studio.tasks.filter(task => taskFilter === 'all' || (taskFilter === 'active' ? !terminal(task.status) : terminal(task.status)));

  async function addProject() { if (!projectName.trim() || projectSaving) return; setProjectSaving(true); try { if (await studio.createProject(projectName.trim())) { setProjectForm(false); setProjectName(''); setNotice('新项目已创建，历史与素材独立保存。'); } } finally { setProjectSaving(false); } }
  const legacyChat = <section className="conversation-section"><div className="section-title compact"><h2><span className="small-line"/>旧版单轮记录</h2><select aria-label="选择历史对话" value={studio.selectedRun || ''} onChange={e => studio.setSelectedRun(e.target.value || null)}><option value="">选择旧版运行</option>{legacyRuns.map(run => <option key={run.id} value={run.id}>{run.prompt.slice(0, 36)} · {statusName(run.status)}</option>)}</select></div><div className="history-summary"><span>已加载 {legacyRuns.length} 条对话</span>{studio.historyOffsets.runs !== null && <button className="text-button" disabled={studio.historyLoading.runs} onClick={() => void studio.loadMore('runs')}>{studio.historyLoading.runs ? '加载中…' : '加载更早对话'}</button>}</div>{selectedRun ? <div className="conversation"><div className="user-message"><span>YOU</span><p>{selectedRun.prompt}</p></div><article className="assistant-message"><div className="assistant-label"><span className="mini-brand">✧</span><strong>{historicalModelName(selectedRun)}</strong>{selectedRun.model_version && <small className="history-model-version" title={selectedRun.model_version} aria-label={'受理时模型规格版本：' + selectedRun.model_version}>受理时规格</small>}<span>{selectedRunDemo ? 'DEMO PROVIDER' : 'LIVE PROVIDER'}</span><span className="stream-status">{selectedRun.moderation ? moderationText(selectedRun.moderation, selectedRun.status) : studio.runConnection || statusName(selectedRun.status)}</span></div><ModerationStatus value={selectedRun.moderation} status={selectedRun.status} onRefresh={studio.recover}/>{selectedRunVisible && <div className="stream-output">{selectedRunText || (activeRun ? '正在等待首段内容…' : '此对话尚无输出。')}{activeRun && <span className="stream-cursor"/>}</div>}<BillingStatus value={selectedRun.billing}/>{selectedRunVisible && selectedRun.error && <p className="inline-error">{moderationErrorMessage(selectedRun.error.code) || selectedRun.error.message}</p>}<div className="run-actions">{activeRun && <button className="button secondary" onClick={() => void studio.cancel('run', selectedRun.id)}><Icon name="stop" size={14}/>停止生成</button>}<button className="text-button" onClick={studio.recover}><Icon name="refresh" size={13}/>恢复快照</button>{selectedRunVisible && <button className="text-button" disabled={!selectedRunText} onClick={() => { if (!moderationAllowsText(selectedRun.moderation, selectedRun.status)) return; void navigator.clipboard.writeText(selectedRunText).then(() => setNotice('对话内容已复制。')).catch(() => setNotice('剪贴板不可用，请手动选择文字复制。')); }}>复制内容</button>}</div></article></div> : <div className="conversation-empty"><Icon name="chat" size={23}/><p>把一个想法写下来，这里就会开始一段新的对话。</p></div>}</section>;

  const securityUnsaved = !!prompt.trim() || cloudDirty || draftRecovery.current.hasUnsaved() || libraryRecovery.current.hasUnsaved() || conversationRecovery.current.hasUnsaved() || references.currentPending || !!studio.pending || generationRecovery.current.hasFor(operationsOwner);
  if (recoveryOpen) return <AccountRecovery key={recoveryVersion} link={accountLink} capabilities={studio.session?.account_security} onClose={() => { if (accountLink && accountLink.kind !== 'invalid') accountLink.secret.clear(); setAccountLink(null); setRecoveryOpen(false); }} onVerified={() => { if (studio.session?.user) void studio.refreshIdentity(); }} onPasswordReset={() => studio.expireSession('密码已重置，全部会话已退出。请使用新密码登录。')}/>;

  if (studio.authState !== 'ready') return <AuthGate checking={studio.authState === 'checking'} loading={studio.loading} busy={studio.authBusy} error={studio.authError} issue={studio.authIssue} onRetry={() => void studio.connect()} onAuthenticate={studio.authenticate} onForgot={() => { setAccountLink(null); setRecoveryVersion(version => version + 1); setRecoveryOpen(true); }}/>

  return <div className={'studio-shell ' + (inspectorOpen && view !== 'operations' && view !== 'generation' ? '' : 'inspector-collapsed')}>
    <a className="skip-link" href="#main-content">跳到创作区域</a>
    <header className="topbar"><div className="brand-cluster"><button className="icon-button mobile-toggle" onClick={() => setSidebarOpen(!sidebarOpen)} aria-label="打开创作导航" aria-expanded={sidebarOpen}><Icon name="menu"/></button><button className="brand" onClick={() => navigate('image')} aria-label="星图 AI Studio 首页"><span className="brand-mark"><svg viewBox="0 0 36 36" fill="none" aria-hidden="true"><path d="M18 2 33 10v16L18 34 3 26V10Z" stroke="currentColor"/><path d="m18 8 9 10-9 10-9-10Z" fill="currentColor" opacity=".9"/><path d="m3 10 15 8 15-8M18 18v16" stroke="currentColor" opacity=".3"/></svg></span><span>星图 <b>AI Studio</b></span></button></div><nav className="top-links" aria-label="主要导航"><button className={isCreator || view === 'canvas' ? 'active' : ''} onClick={() => navigate('image')}>创作工作台</button><button className={view === 'assets' ? 'active' : ''} onClick={() => navigate('assets')}>作品素材</button><button onClick={() => { navigate('image'); setNotice('选择下方灵感参考，填入提示词开始创作。'); }}>灵感参考</button></nav><div className="topbar-right"><span className={'provider-badge ' + (!studio.bootstrap ? 'offline' : '')}><i/>{studio.bootstrap ? demo ? '演示 Provider' : '真实 Provider' : '未连接服务'}</span><button className="icon-button" onClick={() => setInspectorOpen(!inspectorOpen)} aria-label="切换创作设置" aria-expanded={inspectorOpen}><Icon name="sliders"/></button><details className="account-menu"><summary aria-label="账户菜单"><span className="profile">{(studio.session?.user?.name || 'ST').slice(0, 2).toUpperCase()}</span><span className="account-name">{studio.session?.user?.name || '本地演示'}</span></summary><div className="account-popover"><strong>{studio.session?.user?.name || '本地演示工作区'}</strong><p>{studio.session?.user?.email || '当前服务未启用登录验证'}</p><button className="button secondary" disabled={securityBusy} onClick={() => navigate('security')}>账户安全</button>{studio.session?.auth_enabled && <button className="button secondary" disabled={studio.authBusy} onClick={() => guardProjectLeave(() => void studio.logout())}>{studio.authBusy ? '正在退出…' : '退出登录'}</button>}</div></details></div></header>
    <aside className={'sidebar ' + (sidebarOpen ? 'is-open' : '')} aria-label="创作导航"><div className="workspace-switch"><span className="workspace-icon"><Icon name="folder" size={17}/></span><span><strong>{studio.bootstrap?.workspace.name || '个人创作空间'}</strong><small>CREATIVE WORKSPACE</small></span></div><div className="project-picker"><label htmlFor="project-select">当前项目</label><div><select id="project-select" aria-label="切换项目" value={studio.bootstrap?.project.id || ''} disabled={studio.loading} onChange={e => { const id = e.target.value; guardProjectLeave(() => { setPrompt(''); void studio.switchProject(id); }); }}>{!studio.bootstrap && <option value="">正在切换…</option>}{studio.projects.map(project => <option key={project.id} value={project.id}>{project.name}</option>)}</select><button className="icon-button" aria-label="创建项目" aria-expanded={projectForm} disabled={studio.loading} onClick={() => setProjectForm(!projectForm)}><Icon name="plus" size={16}/></button></div>{projectForm && <form className="new-project-form" onSubmit={event => { event.preventDefault(); guardProjectLeave(() => void addProject()); }}><label htmlFor="project-name">项目名称</label><input id="project-name" autoFocus maxLength={80} value={projectName} onChange={e => setProjectName(e.target.value)} placeholder="例如：森林品牌提案" required disabled={projectSaving}/><button type="submit" className="button primary" disabled={projectSaving || !projectName.trim()}>{projectSaving ? '正在创建…' : '创建并进入'}</button></form>}</div><button className="new-project" onClick={() => { navigate('chat'); setNewChatVersion(value => value + 1); }}><Icon name="plus" size={17}/>新的灵感对话<span>↗</span></button><div className="nav-label">创作空间 <span>WORKSPACE</span></div><nav className="side-nav">{navigation.filter(item => (item.id !== 'operations' || operationScopes(operationsAccess).length > 0) && (item.id !== 'generation' || generationRequired)).map(item => <button key={item.id} className={view === item.id ? 'active' : ''} onClick={() => navigate(item.id)} aria-current={view === item.id ? 'page' : undefined}><Icon name={item.icon}/><span>{item.name}</span><small>{item.id === 'tasks' && activeTasks.length ? activeTasks.length : item.id === 'canvas' ? studio.bootstrap?.storage === 'postgres' ? 'CLOUD' : 'LOCAL' : item.label}</small></button>)}</nav><div className="nav-label model-nav-label">模型目录 <span>{studio.bootstrap?.models.length || '—'}</span></div><div className="model-list">{studio.bootstrap?.models.map(item => <button className={item.id === model?.id && isCreator ? 'active' : ''} key={item.id} aria-disabled={!modelCapability(studio.bootstrap, item).enabled} title={modelCapability(studio.bootstrap, item).reason || item.description} onClick={() => selectModel(item.id)}><span className={'model-symbol ' + item.kind}>{item.kind.charAt(0).toUpperCase()}</span><span><strong>{item.name}</strong><small>{modelCapability(studio.bootstrap, item).enabled ? item.description : '未开放 · ' + (modelCapability(studio.bootstrap, item).reason || item.description)}</small></span></button>) || <p className="sidebar-empty">连接服务后显示可用模型。</p>}</div><div className="sidebar-bottom"><span className="tiny-orbit"/><strong>从一个想法，到完整作品。</strong><p>文字、图像与视频，<br/>在同一个创作空间里生长。</p><div className="build-mark">STUDIO / 0.15 <span>本地开发版</span></div></div></aside>
    {sidebarOpen && <button className="mobile-scrim nav-scrim" aria-label="关闭导航" onClick={() => setSidebarOpen(false)}/>}
    <main className="main" id="main-content" ref={mainRef}><div className="main-inner"><div className="content-topline"><div className="breadcrumbs"><Icon name="folder" size={14}/><span>{studio.bootstrap?.project.name || '创作项目'}</span><span>/</span><strong>{title}</strong></div><button className="text-button refresh-button" onClick={() => { void studio.refresh(); studio.recover(); setNotice('正在从服务端刷新历史与连接。'); }} disabled={!studio.bootstrap}><Icon name="refresh" size={13}/>刷新历史</button></div>
      {studio.error && <div className="error-banner" role="alert"><div><strong>{studio.pending ? '受理状态待确认' : '连接或请求需要处理'}</strong><p>{studio.error}</p><RequestDetails error={studio.requestError} unknown={!!studio.pending}/>{studio.pending && <p className="original-request-model" title={studio.pending.body.model_version}>原请求模型：{studio.pending.body.model_id} · 保留原请求规格，目录更新不改变此请求</p>}</div><div className="error-actions"><button className="button secondary" disabled={studio.loading || studio.submitting} onClick={() => { if (!studio.bootstrap) void studio.connect(); else { void studio.refresh(); studio.recover(); } }}>{studio.loading ? '连接中…' : '恢复连接'}</button>{['MODEL_VERSION_CHANGED', 'MODEL_DISABLED', 'INVALID_MODEL_PARAMETERS'].includes(studio.requestError?.code || '') && <button className="button secondary" disabled={studio.catalogLoading} onClick={() => void studio.refreshCatalog()}>刷新模型目录</button>}{studio.pending && <button className="button primary" disabled={studio.submitting || submissionCooldown > 0} onClick={() => void studio.retry()}>{submissionCooldown ? `等待 ${submissionCooldown} 秒` : '原请求重试'}</button>}</div></div>}
      {studio.syncNotice && <div className="mode-banner" role="status"><span>{studio.syncNotice}</span>{studio.syncNotice.includes('恢复') && <button className="text-button" onClick={studio.recover}>恢复同步</button>}</div>}
      <div className={'mode-banner ' + (!studio.bootstrap ? 'waiting' : '')}><span className="mode-light"/><span>{!studio.bootstrap ? studio.loading ? '正在连接本地 Go 服务，读取工作区与模型目录…' : '服务尚未连接，创作请求暂不可用。灵感参考仍可浏览，恢复连接后继续项目创作。' : demo ? '本地演示模式：请求实际经过 Go API；输出来自演示 Provider，不代表真实模型效果。' : '真实 Provider 已启用：创作请求由服务端模型网关处理。'}</span>{studio.bootstrap && <small>{studio.bootstrap.storage === 'memory' ? '内存存储 · 服务重启后清空' : 'PostgreSQL 持久化'}</small>}</div>

      {isCreator && generationRequired && !generationAccess.allowed && <GenerationBanner restricted={generationAccess.data?.state === 'RESTRICTED'} loading={generationAccess.loading} onOpen={() => navigate('generation')}/>}
      {isCreator && studio.bootstrap && !mediaModerationBlocked && mediaUnavailable && <div className="capability-banner"><strong>当前能力暂未开放</strong><p>{currentCapability.reason || '该生成能力尚未配置，请选择其他模式或联系工作区管理员。'}</p></div>}
      {isCreator && view !== 'chat' && <>
        {mediaModerationBlocked ? <ModerationDelivery value={studio.bootstrap?.moderation} media/> : <MediaModerationNotice value={studio.bootstrap?.moderation}/>}
        <section className="creative-heading"><span className="eyebrow"><span/> {headings[kind].eyebrow}</span><h1>{headings[kind].first}<em>{headings[kind].last}</em></h1><p>{headings[kind].description}</p><div className="heading-index">{kind === 'chat' ? '01' : kind === 'image' ? '02' : '03'}<span> / CREATE</span></div></section>
        {kind === 'image' && <div className="image-operation-tabs" role="group" aria-label="图像创作方式"><button type="button" aria-pressed={imageOperation === 'generate'} onClick={() => setImageOperation('generate')}><Icon name="spark" size={15}/>文字生成</button><button type="button" aria-pressed={imageOperation === 'edit'} onClick={() => setImageOperation('edit')}><Icon name="image" size={15}/>参考图编辑</button><span>{imageOperation === 'edit' ? '原图有了，继续你的表达。' : '从一句描述，创造新的画面。'}</span></div>}
        {kind === 'image' && operation === 'edit' && <ReferencePicker state={references.state} selected={references.selected} maxSelected={currentCapability.max_reference_images} limits={studio.bootstrap?.reference_limits} moderation={studio.bootstrap?.moderation} enabled={!!studio.bootstrap?.reference_limits && currentCapability.enabled && !projectSaving && !studio.authBusy && !securityBusy} listing={references.listing} error={references.listError} onFiles={references.addFiles} onSelect={references.select} onRemove={references.remove} onMove={references.move} onRetry={references.retry} onStop={references.stop} onForget={references.forget} onRefresh={references.refresh} onMore={references.loadMore}/>}
        <form className="composer" onSubmit={e => void submit(e)}><div className="composer-label"><Icon name="spark" size={15}/><label htmlFor="creation-prompt">你的创作描述</label><span className={promptTooLong ? 'prompt-over-limit' : ''}>{promptLength.toLocaleString()} / {currentCapability.max_prompt_bytes.toLocaleString()} 字节</span></div><textarea ref={textareaRef} id="creation-prompt" value={prompt} onChange={e => setPrompt(e.target.value)} maxLength={MAX_PROMPT_UNITS} placeholder={operation === 'edit' ? '描述希望怎样编辑参考图：保留哪些主体，改变背景、光线或风格，以及需要保持一致的细节…' : headings[kind].placeholder} onKeyDown={e => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter' && !e.nativeEvent.isComposing) { e.preventDefault(); void submit(); } }}/><div className="composer-footer"><div className="composer-options"><button type="button" className="parameter-chip" onClick={() => setInspectorOpen(true)}><span className={'model-dot ' + kind}/>{model?.name || '等待模型目录'}<span className="chevron">⌄</span></button>{kind !== 'chat' && <button type="button" className="parameter-chip ratio-chip" onClick={() => setInspectorOpen(true)}>{preset?.aspect_ratio || '—'}<span className="separator">/</span>{effectiveCount} {kind === 'video' ? '份' : '张'}</button>}</div><button className="button primary submit-button" type="submit" disabled={!creationInputReady || studio.submitting || quote.loading || !!studio.pending || submissionCooldown > 0}>{studio.submitting ? '正在提交…' : quote.loading ? '正在获取报价…' : submissionCooldown ? `等待 ${submissionCooldown} 秒` : billingEnabled(studio.bootstrap) ? '查看本次报价' : kind === 'chat' ? '开始对话' : kind === 'image' ? operation === 'edit' ? '提交图片编辑' : '生成图像' : '生成视频'}<Icon name="arrow" size={16}/></button></div><div className="composer-bottom-note"><span>{editInvalid ? '请选择并完成参考图上传，确认数量符合模型上限。' : promptTooLong ? '描述超过当前模型上限，请缩短后提交。' : kind === 'chat' ? '中文通常占 3 字节；输出实时流入。' : presetSummary(preset) + ' · 异步处理，切换页面不会中断任务。'}</span><kbd>Ctrl / ⌘ ↵</kbd></div></form>



        {!studio.pending && <QuoteConfirmation {...quote} disabled={studio.submitting || !creationInputReady} onConfirm={accepted => void submit(undefined, accepted)} onRefresh={() => void quote.fetchQuote()} onDismiss={quote.clear}/>}
        {studio.tasks.some(task => task.kind === kind) && <section className="recent-results"><div className="section-title compact"><h2><span className="small-line"/>最近的创作</h2><button className="text-button" onClick={() => navigate('tasks')}>全部任务 <Icon name="arrow" size={14}/></button></div><div className="task-grid">{studio.tasks.filter(task => task.kind === kind).slice(0, 2).map(task => <TaskCard key={task.id} task={task} demo={demo} canCancel={canCancelResource(task, studio.bootstrap?.mode || 'demo')} onCancel={id => studio.cancel('task', id)} onReconcile={studio.reconcileTask} onRefresh={studio.refreshTask}/>)}</div></section>}

        <section className="inspiration-section"><div className="section-title compact"><div><h2><span className="small-line"/>灵感，从这里生长</h2><p>选择一个方向，再加入你的表达。</p></div><span className="reference-label">原创 SVG · 灵感参考</span></div><div className="inspiration-grid">{templates.map((template, index) => <button className="inspiration-card" key={template.variant} onClick={() => applyTemplate(index)} aria-label={'使用模板：' + template.title}><div className="art-frame"><Artwork variant={template.variant}/><span className="use-template"><Icon name="plus" size={15}/>填入提示词</span><span className="art-index">0{index + 1}</span></div><div className="inspiration-copy"><small>{template.tag}</small><strong>{template.title}</strong><Icon name="arrow" size={15}/></div></button>)}</div><p className="reference-footnote">参考画面为本站原创矢量插画，独立于模型输出。真实生成结果只出现在任务与作品素材中。</p></section>
      </>}

      {studio.bootstrap && (view === 'chat' || chatOpened) && <ChatWorkspace key={(studio.session?.user?.id || 'demo') + ':' + studio.bootstrap.project.id} bootstrap={studio.bootstrap} recovery={conversationRecovery.current} model={resolveSelection(studio.bootstrap, 'chat', preferences.chat).model} runs={studio.runs} visible={view === 'chat'} refreshVersion={studio.updates.conversations} newVersion={newChatVersion} connection={studio.runConnection} onRun={studio.receiveRun} onWatch={studio.setSelectedRun} onCancel={id => void studio.cancel('run', id)} onRecover={studio.recover} onAuthExpired={studio.expireSession} onSettings={() => setInspectorOpen(true)} legacy={legacyChat} generationAllowed={generationAccess.allowed}/> }

      {view === 'tasks' && <section className="collection-page"><div className="section-title"><div><span className="eyebrow">EVERY IDEA, IN MOTION</span><h1>创作正在发生。</h1><p>所有图片与视频任务，统一追踪、恢复和管理。</p></div><span className="large-count">{String(studio.tasks.length).padStart(2, '0')}</span></div><div className="filter-tabs" aria-label="任务过滤">{([['all', '全部任务'], ['active', '进行中'], ['complete', '已结束']] as const).map(([id, name]) => <button key={id} className={taskFilter === id ? 'active' : ''} onClick={() => setTaskFilter(id)} aria-pressed={taskFilter === id}>{name}</button>)}</div>{filteredTasks.length ? <div className="task-grid">{filteredTasks.map(task => <TaskCard key={task.id} task={task} demo={demo} canCancel={canCancelResource(task, studio.bootstrap?.mode || 'demo')} onCancel={id => studio.cancel('task', id)} onReconcile={studio.reconcileTask} onRefresh={studio.refreshTask}/>)}</div> : <div className="empty-state"><Icon name="folder" size={32}/><h2>这里还没有{taskFilter === 'active' ? '进行中的' : ''}任务</h2><p>每一次图像与视频创作，都会在这里留下过程。</p><button className="button secondary" onClick={() => navigate('image')}>创建一张图像 <Icon name="arrow" size={15}/></button></div>}</section>}

      {view === 'assets' && studio.bootstrap && <LibraryPage key={(studio.session?.user?.id || 'demo') + ':' + studio.bootstrap.project.id} owner={studio.session?.user?.id || 'demo'} project={studio.bootstrap.project.id} recovery={libraryRecovery.current} refreshVersion={studio.updates.library} onAuthExpired={studio.expireSession}/>}
      {view === 'tasks' && <div className="history-footer"><span>已加载 {studio.tasks.length} 个任务 · 当前列表按已加载记录筛选</span>{studio.historyOffsets.tasks !== null ? <button className="button secondary" disabled={studio.historyLoading.tasks} onClick={() => void studio.loadMore('tasks')}>{studio.historyLoading.tasks ? '加载中…' : '加载更早记录'}<Icon name="arrow" size={14}/></button> : <small>{studio.loading ? '正在读取历史' : '已读取到当前历史末尾'}</small>}</div>}{studio.bootstrap && (view === 'canvas' || draftOpened) && <div hidden={view !== 'canvas'}>{studio.bootstrap.storage === 'postgres' ? <CloudDraftCanvas key={(studio.session?.user?.id || 'demo') + studio.bootstrap.project.id} projectId={studio.bootstrap.project.id} localDraftKey={'xingtu-local-draft-v2:' + (studio.session?.user?.id || 'demo') + ':' + studio.bootstrap.project.id} recovery={draftRecovery.current} suspended={projectSaving || studio.authBusy || securityBusy} refreshVersion={studio.updates.draft} visible={view === 'canvas'} onDirtyChange={setCloudDirty} onAuthExpired={studio.expireSession}/> : <DraftCanvas key={(studio.session?.user?.id || 'demo') + studio.bootstrap.project.id} draftKey={'xingtu-local-draft-v2:' + (studio.session?.user?.id || 'demo') + ':' + studio.bootstrap.project.id}/>}</div>}{view === 'activity' && studio.bootstrap && <ActivityPage key={(studio.session?.user?.id || 'demo') + studio.bootstrap.project.id} projectId={studio.bootstrap.project.id} userId={studio.session?.user?.id} refreshVersion={studio.updates.activity} onAuthExpired={studio.expireSession}/>}
      {view === 'billing' && studio.bootstrap && <BillingPage key={(studio.session?.user?.id || 'demo') + ':' + studio.bootstrap.workspace.id + ':' + studio.bootstrap.project.id} bootstrap={studio.bootstrap} onExpired={studio.expireSession}/>}
      {view === 'operations' && operationsOwner && <LazyPage key={operationsOwner} load={loadOperations} pageProps={{ owner: operationsOwner, access: operationsAccess || { enabled: false, access: [] }, recovery: operationsRecovery.current, generationRecovery: generationRecovery.current, onExpired: studio.expireSession, onBusy: setOperationsBusy, onRefreshAccess: () => setOperationsAccessVersion(value => value + 1), onGenerationChanged: generationAccess.refresh }}/>}
      {view === 'generation' && generationRequired && studio.bootstrap && <LazyPage key={operationsOwner + ':' + studio.bootstrap.workspace.id} load={loadGeneration} pageProps={{ owner: operationsOwner, workspace: studio.bootstrap.workspace.id, controller: generationAccess, recovery: generationRecovery.current, onExpired: studio.expireSession, onBusy: setOperationsBusy }}/>}
      {studio.session && (view === 'security' || securityOpened) && <AccountSecurityPage key={studio.session.user?.id || 'local'} session={studio.session} visible={view === 'security'} hasUnsaved={securityUnsaved} onBusy={setSecurityBusy} onExpired={studio.expireSession} onRefreshIdentity={studio.refreshIdentity}/> }
      <footer className="main-footer"><span>星图 · YOUR CREATIVE UNIVERSE</span><span>MADE FOR THE WAY YOU THINK</span></footer>
    </div></main>

    {view !== 'operations' && view !== 'generation' && inspectorOpen && <button className="mobile-scrim inspector-scrim" aria-label="关闭创作设置" onClick={() => setInspectorOpen(false)}/>}
    <aside hidden={view === 'operations' || view === 'generation'} style={(view === 'operations' || view === 'generation') ? { display: 'none' } : undefined} className={'inspector ' + (inspectorOpen ? 'is-open' : '')} aria-label="创作设置"><div className="inspector-title"><div><Icon name="sliders" size={15}/><strong>创作设置</strong></div><button className="icon-button" aria-label="收起创作设置" onClick={() => setInspectorOpen(false)}><Icon name="close" size={15}/></button></div><ModelControls bootstrap={studio.bootstrap} kind={kind} operation={operation} model={model} selection={selection} presets={presets} preset={preset} creator={isCreator} loading={studio.catalogLoading} error={studio.catalogError} updatedAt={studio.catalogUpdatedAt} onModel={selectModel} onSelection={value => setPreferences(old => ({ ...old, [kind]: value }))} onRefresh={() => void studio.refreshCatalog()}/><RuntimePanel key={(studio.session?.user?.id || 'demo') + ':' + (studio.bootstrap?.project.id || '')} projectId={studio.bootstrap?.project.id || ''} workspaceId={studio.bootstrap?.workspace.id || ''} models={studio.bootstrap?.models || []} enabled={view !== 'operations' && view !== 'generation' && inspectorOpen && !!studio.bootstrap} onAuthExpired={studio.expireSession}/><div className="inspector-section inspector-queue"><div className="queue-heading"><span className="section-number">LIVE / TASKS</span><button className="text-button" onClick={() => navigate('tasks')}>查看全部 <Icon name="arrow" size={13}/></button></div><h3>创作队列 <span>{activeTasks.length}</span></h3>{studio.tasks.slice(0, 3).map(task => <button className="mini-task" key={task.id} onClick={() => navigate('tasks')}><span className="mini-task-icon"><Icon name={task.kind} size={15}/></span><span><strong>{task.prompt}</strong><small>{statusName(task.status)}</small></span><i className={terminal(task.status) ? 'done' : ''}/></button>)}{!studio.tasks.length && <div className="queue-empty"><span>◎</span><p>保持灵感流动。<br/>任务状态将在这里同步。</p></div>}</div><div className="inspector-callout"><span className="callout-line"/><strong>好的作品，来自反复探索。</strong><p>先明确画面，再调试参数。保留有意思的意外，让每个版本都向前一步。</p></div><div className="runtime-info"><span>连接 <b>{studio.bootstrap ? studio.taskConnection : '等待服务'}</b></span><span>存储 <b>{studio.bootstrap?.storage === 'postgres' ? 'PostgreSQL' : studio.bootstrap ? '内存 · 重启清空' : '—'}</b></span></div></aside>
    <footer className="statusbar"><button onClick={() => navigate('tasks')}><span className={'connection-dot ' + (studio.taskConnection === '实时已连接' ? 'connected' : '')}/>{studio.bootstrap ? studio.taskConnection : '本地服务未连接'}<span className="status-separator"/>任务中心 <b>{activeTasks.length}</b>{activeTasks.length ? <span className="status-detail">个任务进行中</span> : <span className="status-detail">等待新的创作</span>}</button><span>{studio.bootstrap?.mode === 'live' ? 'LIVE' : 'LOCAL DEMO'}<i/>GO API + REACT</span></footer>
    {notice && <div className="toast" role="status">{notice}</div>}<dialog ref={leaveDialogRef} className="draft-leave-dialog" aria-labelledby="leave-draft-title" onCancel={event => { event.preventDefault(); setPendingLeave(null); }}><span className="eyebrow">YOUR IDEAS ARE STILL HERE</span><h2 id="leave-draft-title">还有未保存或待确认的内容</h2><p>{cloudDirty ? '离开项目会丢弃当前未保存草稿，可以先保存或导出。' : '可以先留在当前项目处理待确认上传。'}{references.currentPending && '切换会停止当前传输；本页保留原文件供返回后重试，服务器已存文件不会删除。'}已送出的请求不会因离开而撤回。</p><div><button className="button primary" autoFocus onClick={() => { setPendingLeave(null); if (cloudDirty) navigate('canvas'); }}>留在当前项目</button><button className="button secondary" onClick={() => { const action = pendingLeave; draftRecovery.current.discard('xingtu-local-draft-v2:' + (studio.session?.user?.id || 'demo') + ':' + studio.bootstrap?.project.id); setPendingLeave(null); setCloudDirty(false); action?.(); }}>{cloudDirty ? '放弃草稿编辑并继续' : '继续离开，保留待确认上传'}</button></div></dialog>
    <dialog ref={operationsLeaveRef} className="draft-leave-dialog" aria-labelledby="operations-leave-title" onCancel={event => { event.preventDefault(); setOperationsLeave(null); }}><span className="eyebrow">OPERATION / AWAITING CONFIRMATION</span><h2 id="operations-leave-title">还有待核对的运营处置</h2><p>已发送的操作不会因离开而撤回。运营执行只读取原收据核对；用户申请保留原键，未提交说明仍留在本页内存。密码会清空；核对编号仅留在当前页面内存，同一账户返回后可继续查看，刷新或关闭页面会丢失此内存记录。</p><div><button className="button primary" autoFocus onClick={() => setOperationsLeave(null)}>留在此处核对</button><button className="button secondary" onClick={() => { const action = operationsLeave; setOperationsLeave(null); action?.(); }}>离开，保留核对编号</button></div></dialog>
  </div>;
}
