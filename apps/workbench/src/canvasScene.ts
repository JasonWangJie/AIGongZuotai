import type { CanvasDocument, CanvasNode, CanvasGroup } from './freeCanvas';

export const nodeWidth = 176,
  nodeHeight = 96;
export type Camera = { x: number; y: number; scale: number };
export type Box = { x: number; y: number; right: number; bottom: number };
export type Point = { x: number; y: number };
export type CanvasItem = Point & { id: string; node?: CanvasNode; group?: CanvasGroup; count: number };
export type Connection = { from: CanvasItem; to: CanvasItem };
type Located<T> = { box: Box; value: T };
type Tree<T> = { box: Box; leaves?: Located<T>[]; left?: Tree<T>; right?: Tree<T> };

const intersects = (a: Box, b: Box) => a.x <= b.right && a.right >= b.x && a.y <= b.bottom && a.bottom >= b.y;
export function extent<T>(items: Located<T>[]): Box {
  const total = { x: Infinity, y: Infinity, right: -Infinity, bottom: -Infinity };
  for (const { box } of items) {
    total.x = Math.min(total.x, box.x);
    total.y = Math.min(total.y, box.y);
    total.right = Math.max(total.right, box.right);
    total.bottom = Math.max(total.bottom, box.bottom);
  }
  return items.length ? total : { x: 0, y: 0, right: nodeWidth, bottom: nodeHeight };
}
function build<T>(items: Located<T>[]): Tree<T> {
  const box = extent(items);
  if (items.length <= 12) return { box, leaves: items };
  const horizontal = box.right - box.x >= box.bottom - box.y;
  items.sort((a, b) =>
    horizontal
      ? a.box.x + a.box.right - (b.box.x + b.box.right)
      : a.box.y + a.box.bottom - (b.box.y + b.box.bottom),
  );
  const middle = items.length >> 1;
  return { box, left: build(items.slice(0, middle)), right: build(items.slice(middle)) };
}
function collect<T>(tree: Tree<T>, box: Box, into: T[]) {
  if (!intersects(tree.box, box)) return;
  if (tree.leaves) {
    for (const item of tree.leaves) if (intersects(item.box, box)) into.push(item.value);
  } else {
    collect(tree.left!, box, into);
    collect(tree.right!, box, into);
  }
}
export const bounds = (v: Point): Box => ({
  x: v.x,
  y: v.y,
  right: v.x + nodeWidth,
  bottom: v.y + nodeHeight,
});
export const center = (v: Point): Point => ({ x: v.x + nodeWidth / 2, y: v.y + nodeHeight / 2 });

// Clip against the viewport on both axes, including a long edge joining two
// offscreen nodes whose bounding box happens to cover the visible rectangle.
export function crosses(a: Point, b: Point, box: Box) {
  let t0 = 0,
    t1 = 1;
  for (const [start, delta, low, high] of [
    [a.x, b.x - a.x, box.x, box.right],
    [a.y, b.y - a.y, box.y, box.bottom],
  ]) {
    if (delta === 0) {
      if (start < low || start > high) return false;
      continue;
    }
    const first = (low - start) / delta,
      last = (high - start) / delta;
    t0 = Math.max(t0, Math.min(first, last));
    t1 = Math.min(t1, Math.max(first, last));
    if (t0 > t1) return false;
  }
  return true;
}

export class CanvasScene {
  readonly items = new Map<string, CanvasItem>();
  readonly adjacency = new Map<string, Connection[]>();
  readonly box: Box;
  readonly detailLimit: number;
  private nodes: Tree<CanvasItem>;
  private edges: Tree<Connection>;
  constructor(doc: CanvasDocument) {
    const collapsed = new Map(doc.groups.filter((g) => g.collapsed).map((g) => [g.id, g]));
    const displayed = new Map<string, CanvasItem>();
    for (const node of doc.nodes) {
      const group = node.group_id ? collapsed.get(node.group_id) : undefined;
      const id = group ? '!' + group.id : node.id;
      let item = this.items.get(id);
      if (!item) {
        item = { id, x: node.x, y: node.y, node: group ? undefined : node, group, count: 0 };
        this.items.set(id, item);
      }
      item.x = Math.min(item.x, node.x);
      item.y = Math.min(item.y, node.y);
      item.count++;
      displayed.set(node.id, item);
    }
    const points = [...this.items.values()].map((value) => ({ value, box: bounds(value) }));
    this.box = extent(points);
    this.nodes = build(points);
    const edges: Located<Connection>[] = [];
    const pairs = new Set<string>();
    for (const edge of doc.edges) {
      const from = displayed.get(edge.from),
        to = displayed.get(edge.to);
      if (!from || !to || from === to) continue;
      const pair = from.id + ' ' + to.id;
      if (pairs.has(pair)) continue;
      pairs.add(pair);
      const a = center(from),
        b = center(to),
        value = { from, to };
      edges.push({
        value,
        box: {
          x: Math.min(a.x, b.x),
          y: Math.min(a.y, b.y),
          right: Math.max(a.x, b.x),
          bottom: Math.max(a.y, b.y),
        },
      });
      for (const id of [from.id, to.id]) {
        const list = this.adjacency.get(id) || [];
        list.push(value);
        this.adjacency.set(id, list);
      }
    }
    this.edges = build(edges);
    this.detailLimit = doc.nodes.length > 500 || doc.edges.length > 1000 ? 100 : 150;
  }
  view(box: Box, scale: number, selected: string, drag?: { id: string; point: Point }) {
    let items: CanvasItem[] = [];
    const edges: Connection[] = [];
    collect(this.nodes, box, items);
    collect(this.edges, box, edges);
    if (drag) {
      items = items.filter((n) => n.id !== drag.id);
      const original = this.items.get(drag.id);
      if (original && intersects(bounds(drag.point), box)) items.push({ ...original, ...drag.point });
    }
    const candidates = drag ? new Set([...edges, ...(this.adjacency.get(drag.id) || [])]) : edges;
    const connections: Connection[] = [];
    const position = (item: CanvasItem) => (drag?.id === item.id ? { ...item, ...drag.point } : item);
    for (const edge of candidates) {
      const from = position(edge.from),
        to = position(edge.to);
      if (crosses(center(from), center(to), box)) connections.push({ from, to });
    }
    const priority = (n: CanvasItem) =>
      n.id === selected ? 3 : n.node?.asset_id || n.node?.run_id ? 2 : n.group ? 1 : 0;
    const detailed = items
      .filter((n) => scale >= 0.45 || n.id === selected)
      .sort((a, b) => priority(b) - priority(a))
      .slice(0, this.detailLimit);
    const detailedIDs = new Set(detailed.map((n) => n.id));
    return { detailed, overview: items.filter((n) => !detailedIDs.has(n.id)), edges: connections };
  }
  hit(point: Point) {
    const items: CanvasItem[] = [];
    collect(this.nodes, { ...point, right: point.x, bottom: point.y }, items);
    return items.at(-1);
  }
}
export function worldPoint(camera: Camera, x: number, y: number): Point {
  return { x: (x - camera.x) / camera.scale, y: (y - camera.y) / camera.scale };
}
export function zoomAt(camera: Camera, point: Point, scale: number): Camera {
  const next = Math.max(0.00001, Math.min(2, scale));
  const world = worldPoint(camera, point.x, point.y);
  return { x: point.x - world.x * next, y: point.y - world.y * next, scale: next };
}
export function fit(box: Box, width: number, height: number): Camera {
  const scale = Math.max(
    0.00001,
    Math.min(
      1.3,
      (width - 48) / Math.max(1, box.right - box.x),
      (height - 48) / Math.max(1, box.bottom - box.y),
    ),
  );
  return {
    scale,
    x: (width - (box.right - box.x) * scale) / 2 - box.x * scale,
    y: (height - (box.bottom - box.y) * scale) / 2 - box.y * scale,
  };
}
