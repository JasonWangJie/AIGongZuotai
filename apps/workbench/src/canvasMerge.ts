import { uiText } from './locale';
import type { CanvasDocument } from './freeCanvas';

// Canvas records contain only scalar writable fields. A missing optional field
// and an undefined value represent the same PUT payload.
function equal<T extends { id: string }>(a: T | undefined, b: T | undefined) {
  if (!a || !b) return a === b;
  const keys = new Set([...Object.keys(a), ...Object.keys(b)]) as Set<keyof T>;
  for (const k of keys) if (a[k] !== b[k]) return false;
  return true;
}
export function mergeCanvas(
  base: CanvasDocument,
  local: CanvasDocument,
  remote: CanvasDocument,
  prefer: 'local' | 'remote',
) {
  const conflicts: string[] = [];
  function records<T extends { id: string }>(name: string, before: T[], mine: T[], server: T[]) {
    const byId = (rows: T[]) => new Map(rows.map((row) => [row.id, row]));
    const b = byId(before),
      m = byId(mine),
      r = byId(server);
    const out: T[] = [];
    for (const id of new Set([...r.keys(), ...m.keys()])) {
      const a = b.get(id),
        l = m.get(id),
        s = r.get(id);
      const clash = !equal(a, l) && !equal(a, s) && !equal(l, s);
      if (clash) conflicts.push(name + ' ' + id);
      const choice = equal(a, l) ? s : clash && prefer === 'remote' ? s : l;
      if (choice) out.push(choice);
    }
    return out;
  }
  const localTitle = base.title !== local.title,
    remoteTitle = base.title !== remote.title;
  const titleConflict = localTitle && remoteTitle && local.title !== remote.title;
  if (titleConflict) conflicts.push(uiText('画布名称'));
  const doc: CanvasDocument = {
    title: localTitle && (!titleConflict || prefer === 'local') ? local.title : remote.title,
    nodes: records(uiText('节点'), base.nodes, local.nodes, remote.nodes),
    edges: records(uiText('连线'), base.edges, local.edges, remote.edges),
    groups: records(uiText('分组'), base.groups, local.groups, remote.groups),
  };
  return { doc, conflicts };
}
