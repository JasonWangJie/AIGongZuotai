// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { beforeEach, afterEach, it, expect, vi } from 'vitest';
import { collabApi, checkDraft, checkReceipt, relationSlot, draftSlot, readTicket } from './collaboration';
import type { Member, RelationTicket } from './collaboration';
import { ProjectCollaboration } from './ProjectCollaboration';
import { SharedDraftEditor } from './SharedDraftEditor';
import type { DraftAccess } from './SharedDraftEditor';
import { ApiError } from './api';
import type { CloudDraft } from './types';
const noop = () => {},
  u = 'usr_' + 'a'.repeat(32),
  me = 'prj_' + 'a'.repeat(32),
  host = 'prj_' + 'b'.repeat(32),
  inviteID = 'pci_' + 'b'.repeat(32);
const access: DraftAccess = { id: host, name: '共享画布', role: 'editor', round: inviteID };
const member: Member = {
  target_user_id: u,
  project_id: host,
  project_name: '共享画布',
  inviter_name: '合作者',
  invitation_id: inviteID,
  role: 'editor',
  state: 'ACTIVE',
  round: 1,
  version: 2,
};
const d = (revision: number, note = '正文'): CloudDraft => ({
  project_id: host,
  revision,
  updated_at: null,
  nodes: [{ id: 'n1', title: '节点', note }],
});
const props = {
  user: u,
  project: me,
  projectName: '我的项目',
  visible: true,
  enabled: true,
  onDirty: noop,
  onExpired: noop,
};
let root: ReturnType<typeof createRoot>, view: HTMLDivElement;
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  sessionStorage.clear();
  view = document.createElement('div');
  document.body.append(view);
  root = createRoot(view);
});
afterEach(async () => {
  await act(() => root.unmount());
  view.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function btn(label: string) {
  return [...view.querySelectorAll('button')].find((b) => b.textContent === label);
}
async function click(label: string) {
  const b = btn(label);
  expect(b, label).toBeDefined();
  expect(b!.disabled).toBe(false);
  await act(() => b!.click());
}
async function field(label: string, value: string) {
  const el = view.querySelector('[aria-label="' + label + '"]') as
    HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
  expect(el, label).toBeTruthy();
  await act(() => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value')!.set!.call(el, value);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
  });
}
function setup() {
  vi.spyOn(collabApi, 'members').mockResolvedValue({ items: [] });
  vi.spyOn(collabApi, 'invitations').mockResolvedValue({ items: [] });
  vi.spyOn(collabApi, 'projects').mockResolvedValue({ items: [member] });
  vi.spyOn(collabApi, 'draft').mockResolvedValue(d(1));
  vi.spyOn(collabApi, 'history').mockResolvedValue({ items: [], has_more: false });
}
const page = () => act(() => root.render(<ProjectCollaboration {...props} />));
const editor = (a = access) =>
  act(() =>
    root.render(
      <SharedDraftEditor
        user={u}
        access={a}
        visible={true}
        onDirty={noop}
        onDenied={noop}
        onExpired={noop}
      />,
    ),
  );
it('sends shared and admin routes without a host project override in header or query', async () => {
  const f = vi
    .fn()
    .mockImplementation(
      async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }),
    );
  vi.stubGlobal('fetch', f);
  const t: RelationTicket = {
    project: host,
    target: u,
    operation: 'accept',
    expected: 1,
    id: inviteID,
    key: 'original-key-123',
  };
  await collabApi.mutate(t);
  await collabApi.receipt(t);
  await collabApi.draft(host);
  await collabApi.save(host, { expected_revision: 1, nodes: [] }, t.key);
  await collabApi.saved(host, t.key);
  for (const [path, init] of f.mock.calls) {
    expect(path).not.toContain('project_id=');
    expect(new Headers(init.headers).has('X-Project-ID')).toBe(false);
  }
  expect(f.mock.calls.map((c) => c[1].method || 'GET')).toEqual(['POST', 'GET', 'GET', 'PUT', 'GET']);
  expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({ expected_version: 1 });
});
it.each(['target', 'project', 'version', 'operation', 'round'])(
  'rejects a mismatched membership %s or operation key read',
  (which) => {
    const t: RelationTicket = {
      operation: 'accept',
      project: host,
      target: u,
      expected: 1,
      id: inviteID,
      key: 'original-key-123',
    };
    const r = { ...member, operation: 'accept' as const };
    if (which === 'target') r.target_user_id = 'another';
    if (which === 'project') r.project_id = me;
    if (which === 'version') r.version = 3;
    if (which === 'operation') Object.assign(r, { operation: 'invite' });
    if (which === 'round') r.invitation_id = 'old';
    expect(() => checkReceipt(r, t)).toThrow();
  },
);
it('reads historic receipt only, without inserting an old grant into active projects', async () => {
  setup();
  vi.mocked(collabApi.projects).mockResolvedValue({ items: [] });
  const t: RelationTicket = {
    operation: 'accept',
    project: host,
    target: u,
    id: inviteID,
    expected: 1,
    key: 'confirmed-key-12',
  };
  sessionStorage.setItem(relationSlot(u), JSON.stringify(t));
  const save = vi.spyOn(collabApi, 'mutate');
  vi.spyOn(collabApi, 'receipt').mockResolvedValue({ ...member, operation: 'accept' });
  await page();
  await click('只读核对原成员收据');
  expect(view.textContent).toContain('当时已加入');
  expect(btn('打开共享草稿')).toBeUndefined();
  expect(save).not.toHaveBeenCalled();
  expect(readTicket(relationSlot(u))).toBeNull();
});
it('drops closed-project content and editor controls on downgrade and revoke', async () => {
  setup();
  await page();
  await click('打开共享草稿');
  expect(view.querySelector('[aria-label="节点正文 1"]')).toBeTruthy();
  await field('节点正文 1', '不能留在只读编辑器的本页输入');
  vi.mocked(collabApi.projects).mockResolvedValue({ items: [{ ...member, role: 'viewer', version: 3 }] });
  await click('刷新协作权限');
  expect(view.querySelector('.co-editor textarea')).toBeNull();
  expect(view.querySelector('.co-editor')?.textContent).toContain('正文');
  expect(view.textContent).not.toContain('不能留在只读编辑器的本页输入');
  vi.mocked(collabApi.projects).mockResolvedValue({ items: [] });
  await click('刷新协作权限');
  expect(view.querySelector('.co-editor')).toBeNull();
  expect(btn('打开共享草稿')).toBeUndefined();
});
it('rejects a foreign project list and never loads its contents', async () => {
  setup();
  vi.mocked(collabApi.projects).mockResolvedValue({ items: [{ ...member, target_user_id: 'other' }] });
  await page();
  expect(btn('打开共享草稿')).toBeUndefined();
  expect(collabApi.draft).not.toHaveBeenCalled();
});
it('keeps the original node edit across a genuine CAS conflict and requires explicit resolution', async () => {
  setup();
  const save = vi
    .spyOn(collabApi, 'save')
    .mockRejectedValueOnce(new ApiError('版本已变', 409, false, 'DRAFT_CONFLICT'))
    .mockResolvedValueOnce(d(3, '本页编辑'));
  await editor();
  await field('节点正文 1', '本页编辑');
  vi.mocked(collabApi.draft).mockResolvedValue(d(2, '服务器的新内容'));
  await click('保存共享草稿');
  expect(view.querySelector('.co-warn details')?.textContent).toContain('服务器的新内容');
  expect((view.querySelector('textarea') as HTMLTextAreaElement).value).toBe('本页编辑');
  expect(btn('保存共享草稿')?.disabled).toBe(true);
  await click('保留本页并基于最新修订');
  await click('保存共享草稿');
  expect(save.mock.calls.map((call) => call[1].expected_revision)).toEqual([1, 2]);
});
it('stores only the revision and key when a shared save is uncertain, never retries the PUT and preserves another round', async () => {
  setup();
  const save = vi.spyOn(collabApi, 'save').mockRejectedValue(new ApiError('回执待核对', 0, true));
  const receipt = vi.spyOn(collabApi, 'saved').mockResolvedValue(d(2, '私有内容'));
  const old = draftSlot(u, host, 'older');
  sessionStorage.setItem(old, JSON.stringify({ expected: 0, key: 'other-round-key' }));
  await editor();
  await field('节点正文 1', '私有内容');
  await click('保存共享草稿');
  const slot = draftSlot(u, host, inviteID);
  expect(sessionStorage.getItem(slot)).not.toContain('私有内容');
  await click('只读核对原草稿收据');
  expect(receipt).toHaveBeenCalledWith(host, save.mock.calls[0][2]);
  expect(save).toHaveBeenCalledTimes(1);
  expect(sessionStorage.getItem(slot)).toBeNull();
  expect(sessionStorage.getItem(old)).not.toBeNull();
});
it('does not accept another project or malformed revision as a private shared draft', () => {
  expect(() => checkDraft(d(1), me)).toThrow();
  expect(() => checkDraft(d(1), host, 2)).toThrow();
  expect(() => checkDraft({ ...d(1), nodes: [{ id: 'bad/id', title: '', note: '' }] }, host)).toThrow();
});

it('looks up an off-page expired member by exact target before re-inviting with the correct CAS', async () => {
  setup();
  const target = 'usr_' + 'b'.repeat(32);
  const previous = {
    ...member,
    project_id: me,
    target_user_id: target,
    state: 'EXPIRED' as const,
    version: 7,
  };
  vi.mocked(collabApi.members).mockImplementation(async (_project, id) => ({
    items: id === target ? [previous] : [],
  }));
  const mutate = vi
    .spyOn(collabApi, 'mutate')
    .mockResolvedValue({ ...previous, operation: 'invite', state: 'PENDING', round: 2, version: 8 });
  await page();
  await field('对方账户编号', target);
  await click('邀请协作者');
  expect(collabApi.members).toHaveBeenCalledWith(me, target);
  expect(mutate).not.toHaveBeenCalled();
  await click('确认邀请');
  expect(mutate.mock.calls[0][0].expected).toBe(7);
});
