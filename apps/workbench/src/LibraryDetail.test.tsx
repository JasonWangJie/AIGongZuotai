import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { AssetHistoryPreview, LibraryDetail } from './LibraryDetail';
import { LibraryRecovery } from './libraryState';
import type { AssetEntry, AssetRevision } from './libraryState';
const asset: AssetEntry = { id: 'a', project_id: 'p', source: 'reference', kind: 'image', name: '保留图片', tags: ['原始'], state: 'trashed', revision: 5, created_at: '2026-11-13T00:00:00Z', updated_at: '2026-11-13T00:00:00Z', deleted_at: '2026-11-13T00:00:00Z', content_url: null, thumbnail_url: null };
const render = (entry: AssetEntry) => renderToStaticMarkup(<LibraryDetail asset={entry} owner="u" project="p" recovery={new LibraryRecovery()} onClose={() => {}} onChanged={() => {}} onAuthExpired={() => {}}/>);
describe('recycled asset detail controls', () => {
  it('renders name/tags/save disabled, keeps history readable and leaves restore available', () => { const html = render(asset); expect(html).toMatch(/<input[^>]*disabled=""/); expect(html).toMatch(/<textarea[^>]*disabled=""/); expect(html).toMatch(/<button[^>]*disabled=""[^>]*>保存名称与标签<\/button>/); expect(html).toMatch(/<button(?![^>]*disabled)[^>]*>恢复此素材<\/button>/); expect(html).toContain('请先恢复，再编辑名称、标签或载入历史元数据'); expect(html).toContain('<summary>名称与标签的版本历史</summary>'); expect(html).not.toContain('打开原文件'); });
  it('re-enables metadata fields after the asset is restored', () => { const html = render({ ...asset, state: 'active', deleted_at: null, revision: 6 }); expect(html).toMatch(/<input(?![^>]*disabled)[^>]*value="保留图片"/); expect(html).toMatch(/<textarea(?![^>]*disabled)[^>]*>/); expect(html).not.toContain('请先恢复，再编辑名称'); });
  it('renders a readable old revision but disables applying it while metadata is locked', () => { const revision: AssetRevision = { revision: 2, name: '旧名称', tags: ['旧标签'], state: 'active', updated_at: '' }; const locked = renderToStaticMarkup(<AssetHistoryPreview revision={revision} disabled onLoad={() => {}}/>), editable = renderToStaticMarkup(<AssetHistoryPreview revision={revision} disabled={false} onLoad={() => {}}/>); expect(locked).toContain('旧名称'); expect(locked).toContain('旧标签'); expect(locked).toMatch(/<button[^>]*disabled=""[^>]*>载入这版名称与标签/); expect(editable).toMatch(/<button(?![^>]*disabled)[^>]*>载入这版名称与标签/); });
});
