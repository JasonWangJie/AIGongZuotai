import { describe, expect, it } from 'vitest';
import { CanvasScene, center, crosses, fit, worldPoint, zoomAt } from './canvasScene';
import { emptyCanvas, freeCanvasApi, imagePath, usableSource, validCanvas } from './freeCanvas';
import type { CanvasDocument, CanvasVersion } from './freeCanvas';
import { parseResourceChanged } from './durableEvents';
import { mergeCanvas } from './canvasMerge';

function grid(n: number): CanvasDocument {
  const nodes = Array.from({ length: n }, (_, i) => ({
    id: 'n' + i,
    title: '节点 ' + i,
    x: (i % 40) * 200,
    y: Math.floor(i / 40) * 120,
  }));
  const edges = nodes.flatMap((from, i) =>
    [1, 40].map((d) => ({ id: 'e' + i + '_' + d, from: from.id, to: nodes[(i + d) % n].id })),
  );
  return { ...emptyCanvas(), nodes, edges };
}
describe('project canvas spatial index', () => {
  it('rejects an oversized UTF-8 envelope before opening a save request', async () => {
    const doc = grid(2000);
    doc.nodes = doc.nodes.map((n) => ({ ...n, note: '画'.repeat(600) }));
    await expect(freeCanvasApi.save('p', 0, doc, 'key', new AbortController().signal)).rejects.toMatchObject({
      status: 413,
      uncertain: false,
    });
  });
  it('keeps independent server edits and offers both resolutions for concurrently edited or removed records', () => {
    const base = grid(4),
      local = { ...base, nodes: base.nodes.slice(1).map((n) => ({ ...n, note: '本页' })) };
    const remote = {
      ...base,
      title: '远端标题',
      nodes: base.nodes.map((n, i) => (i < 2 ? { ...n, title: '服务器' } : n)),
    };
    const mine = mergeCanvas(base, local, remote, 'local'),
      server = mergeCanvas(base, local, remote, 'remote');
    expect(mine.conflicts).toEqual(['节点 n0', '节点 n1']);
    expect(mine.doc.nodes[0].id).toBe('n1');
    expect(mine.doc.nodes[0].note).toBe('本页');
    expect(mine.doc.title).toBe('远端标题');
    expect(server.doc.nodes.slice(0, 2)).toEqual(remote.nodes.slice(0, 2));
    expect(server.doc.nodes[2].note).toBe('本页');
  });
  it.each([
    [500, 150],
    [2000, 100],
  ])('bounds detailed cards for %i nodes at full and overview scale', (n, max) => {
    const doc = grid(n),
      scene = new CanvasScene(doc);
    expect(validCanvas(doc)).toBe(true);
    const normal = scene.view(scene.box, 1, 'n0');
    expect(normal.detailed.length).toBe(max);
    expect(normal.overview.length + normal.detailed.length).toBe(n);
    expect(normal.edges.length).toBe(2 * n);
    const overview = scene.view(scene.box, 0.1, 'n10');
    expect(overview.detailed.map((n) => n.id)).toEqual(['n10']);
    expect(overview.overview.length).toBe(n - 1);
    const area = { x: 403, y: 251, right: 780, bottom: 455 };
    const local = scene.view(area, 1, '');
    const reference = doc.edges.filter((e) =>
      crosses(center(doc.nodes[+e.from.slice(1)]), center(doc.nodes[+e.to.slice(1)]), area),
    );
    expect(local.edges.length).toBe(reference.length);
    expect(local.detailed.length).toBeLessThan(10);
  });
  it('keeps crossing edges from outside the viewport, repositions incident edges and eliminates hidden edges', () => {
    const doc: CanvasDocument = {
      title: '测试',
      nodes: [
        { id: 'a', title: 'A', x: -600, y: 0, group_id: 'g' },
        { id: 'b', title: 'B', x: 800, y: 0, group_id: 'g' },
      ],
      groups: [{ id: 'g', title: '分组', collapsed: false }],
      edges: [{ id: 'line', from: 'a', to: 'b' }],
    };
    const box = { x: 0, y: 0, right: 120, bottom: 120 };
    const scene = new CanvasScene(doc);
    expect(scene.view(box, 1, '').edges.length).toBe(1);
    expect(scene.view(box, 1, '', { id: 'a', point: { x: 1000, y: 500 } }).edges.length).toBe(0);
    const collapsed = new CanvasScene({ ...doc, groups: [{ ...doc.groups[0], collapsed: true }] });
    expect([...collapsed.items.keys()]).toEqual(['!g']);
    expect(collapsed.items.get('!g')?.count).toBe(2);
    expect(collapsed.view(collapsed.box, 1, '').edges.length).toBe(0);
  });
  it('fits extreme legal coordinates on narrow screens and keeps world coordinates under wheel zoom', () => {
    const box = { x: -1_000_000, y: 0, right: 1_000_176, bottom: 96 },
      c = fit(box, 320, 350);
    expect(c.x + box.x * c.scale).toBeGreaterThan(0);
    expect(c.x + box.right * c.scale).toBeLessThan(320);
    const next = zoomAt(c, { x: 20, y: 70 }, 0.25);
    expect(worldPoint(next, 20, 70).x).toBeCloseTo(worldPoint(c, 20, 70).x);
  });
  it('validates bounded binding identities, no duplicate directed edge and fixed source URL', () => {
    const doc = grid(4);
    doc.edges = [];
    const node = { ...doc.nodes[0], id: '__proto__', asset_id: 'asset', asset_revision: 2 };
    doc.nodes[0] = node;
    expect(validCanvas(doc)).toBe(true);
    expect(validCanvas({ ...doc, nodes: [{ ...node, run_id: 'bad' }] })).toBe(false);
    const path = '/api/v1/projects/p/canvas/revisions/2/nodes/__proto__/thumbnail?project_id=p';
    const source = { available: true, thumbnail_url: path, content_version: 'f'.repeat(64) };
    const v: CanvasVersion = {
      project_id: 'p',
      revision: 2,
      document: doc,
      sources: { [node.id]: source },
      created_at: '',
    };
    expect(usableSource(node, v, v.sources)).toBe(source);
    expect(usableSource({ ...node, asset_revision: 3 }, v, v.sources)).toBeUndefined();
    expect(imagePath('p', 2, node, source)).toBe(path);
    expect(imagePath('other', 2, node, source)).toBeUndefined();
    expect(
      imagePath('p', 2, node, { ...source, thumbnail_url: 'https://invalid.test/image' }),
    ).toBeUndefined();
    expect(
      validCanvas({
        ...doc,
        edges: [
          { id: 'e', from: node.id, to: 'n1' },
          { id: 'f', from: node.id, to: 'n1' },
        ],
      }),
    ).toBe(false);
  });
  it('accepts only the canvas audit hint paired with the canvas resource', () => {
    const event = {
      event_id: 'ev',
      seq: 1,
      kind: 'audit.created',
      resource_type: 'canvas',
      resource_id: 'prj',
      resource_version: 1,
      action: 'canvas.saved',
    };
    expect(parseResourceChanged(JSON.stringify(event))).toEqual(event);
    expect(parseResourceChanged(JSON.stringify({ ...event, action: 'run.accepted' }))).toBeNull();
    expect(parseResourceChanged(JSON.stringify({ ...event, resource_type: 'draft' }))).toBeNull();
  });
});
