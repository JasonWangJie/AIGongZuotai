import { uiText, useLanguage } from './locale';
import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { PointerEvent as ReactPointerEvent } from 'react';
import { statusName } from './types';
import { coord, imagePath } from './freeCanvas';
import type { CanvasDocument, CanvasSources } from './freeCanvas';
import { CanvasScene, center, fit, nodeHeight, nodeWidth, worldPoint, zoomAt } from './canvasScene';
import type { Camera, CanvasItem, Point } from './canvasScene';

type Args = {
  doc: CanvasDocument;
  project: string;
  revision: number;
  sources: CanvasSources;
  selected: string;
  visible: boolean;
  locked: boolean;
  onSelect: (id: string) => void;
  onMove: (id: string, location: Point) => void;
  focusRequest?: { id: string; ticket: number };
};
type Drag = { id: string; point: Point };
type Gesture = { id: number; start: Point; camera: Camera; item?: CanvasItem };

const ItemCard = memo(function ItemCard({
  item,
  selected,
  source,
  project,
  revision,
  choose,
}: {
  item: CanvasItem;
  selected: boolean;
  source?: CanvasSources[string];
  project: string;
  revision: number;
  choose: (id: string) => void;
}) {
  useLanguage();
  const node = item.node;
  const thumb = node && imagePath(project, revision, node, source);
  const [failedImage, setFailedImage] = useState('');
  useEffect(() => {
    if (!thumb) setFailedImage('');
  }, [thumb]);
  const imageReady = thumb && thumb !== failedImage;
  return (
    <button
      type="button"
      className={'free-canvas-card' + (selected ? ' is-selected' : '') + (item.group ? ' is-group' : '')}
      data-node-id={node?.id}
      data-group-id={item.group?.id}
      aria-pressed={selected}
      onClick={() => choose(item.id)}
      style={{
        width: nodeWidth,
        height: nodeHeight,
        transform: 'translate(' + item.x + 'px,' + item.y + 'px)',
      }}
    >
      <span className="free-card-kind">
        {item.group ? uiText('分组') : node?.asset_id ? uiText('图片') : node?.run_id ? uiText('对话') : uiText('便笺')}
      </span>
      <strong>{node?.title || item.group?.title}</strong>
      {imageReady && (
        <img
          src={thumb}
          alt={uiText('已绑定图片 · ') + node!.title}
          draggable={false}
          onError={() => setFailedImage(thumb)}
        />
      )}
      {node?.run_id && (
        <small className="free-canvas-run" data-status={source?.available ? source.status : undefined}>
          {source?.available && source.status
            ? statusName(source.status)
            : source
              ? uiText('状态不可用')
              : uiText('保存后读取状态')}
        </small>
      )}
      {item.group ? (
        <small>{item.count} {uiText("个节点 · 已折叠")}</small>
      ) : (
        !node?.run_id &&
        !imageReady && (
          <small>{node?.asset_id ? (source ? uiText('预览不可用') : uiText('保存后读取图片')) : node?.note || ''}</small>
        )
      )}
    </button>
  );
});

export function FreeCanvasViewport({
  doc,
  project,
  revision,
  sources,
  selected,
  visible,
  locked,
  onSelect,
  onMove,
  focusRequest,
}: Args) {
  const scene = useMemo(() => new CanvasScene(doc), [doc]);
  const viewport = useRef<HTMLDivElement>(null),
    raster = useRef<HTMLCanvasElement>(null);
  const [size, setSize] = useState({ width: 600, height: 450 });
  const [camera, setCamera] = useState<Camera>(() => ({
    x: 30 - (doc.nodes[0]?.x || 0),
    y: 30 - (doc.nodes[0]?.y || 0),
    scale: 1,
  }));
  const latestCamera = useRef(camera);
  latestCamera.current = camera;
  const [drag, setDrag] = useState<Drag>();
  const gesture = useRef<Gesture | undefined>(undefined),
    frame = useRef(0),
    pending = useRef<() => void>(undefined);
  const frameUpdate = useCallback((apply: () => void) => {
    pending.current = apply;
    if (!frame.current)
      frame.current = requestAnimationFrame(() => {
        frame.current = 0;
        pending.current?.();
        pending.current = undefined;
      });
  }, []);
  const cancelFrame = useCallback(() => {
    cancelAnimationFrame(frame.current);
    frame.current = 0;
    pending.current = undefined;
  }, []);
  useLayoutEffect(() => {
    const el = viewport.current;
    if (!visible || !el) return;
    const measure = () =>
      setSize((old) =>
        old.width === el.clientWidth && old.height === el.clientHeight
          ? old
          : { width: el.clientWidth, height: el.clientHeight },
      );
    measure();
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure);
    observer?.observe(el);
    window.addEventListener('resize', measure);
    return () => {
      observer?.disconnect();
      window.removeEventListener('resize', measure);
      cancelFrame();
      gesture.current = undefined;
    };
  }, [visible, cancelFrame]);
  useEffect(() => {
    if (!visible || locked) {
      cancelFrame();
      gesture.current = undefined;
      setDrag(undefined);
    }
  }, [visible, locked, cancelFrame]);
  useEffect(() => {
    const el = viewport.current;
    if (!el || !visible) return;
    function wheel(e: WheelEvent) {
      e.preventDefault();
      const rect = el!.getBoundingClientRect(),
        point = { x: e.clientX - rect.left, y: e.clientY - rect.top };
      const next = zoomAt(
        latestCamera.current,
        point,
        latestCamera.current.scale * Math.exp(-Math.max(-400, Math.min(400, e.deltaY)) * 0.002),
      );
      latestCamera.current = next;
      frameUpdate(() => setCamera(next));
    }
    el.addEventListener('wheel', wheel, { passive: false });
    return () => el.removeEventListener('wheel', wheel);
  }, [frameUpdate, visible]);
  const lastFocus = useRef(0);
  useEffect(() => {
    if (!focusRequest || lastFocus.current === focusRequest.ticket) return;
    const item = scene.items.get(focusRequest.id);
    if (!item) return;
    lastFocus.current = focusRequest.ticket;
    setCamera((c) => ({
      x: size.width / 2 - center(item).x * c.scale,
      y: size.height / 2 - center(item).y * c.scale,
      scale: c.scale,
    }));
    viewport.current?.focus({ preventScroll: true });
  }, [focusRequest, scene, size]);
  const content = useMemo(() => {
    const a = worldPoint(camera, 0, 0),
      b = worldPoint(camera, size.width, size.height);
    return scene.view({ ...a, right: b.x, bottom: b.y }, camera.scale, selected, drag);
  }, [scene, size, camera, selected, drag]);

  useLayoutEffect(() => {
    const el = raster.current;
    if (!visible || !el || !size.width || !size.height) return;
    const ratio = Math.min(window.devicePixelRatio || 1, 2);
    const width = Math.round(size.width * ratio),
      height = Math.round(size.height * ratio);
    if (el.width !== width) el.width = width;
    if (el.height !== height) el.height = height;
    const ctx = el.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, width, height);
    const scale = ratio * camera.scale;
    ctx.setTransform(scale, 0, 0, scale, ratio * camera.x, ratio * camera.y);
    ctx.strokeStyle = '#686078';
    ctx.lineWidth = 1.2 / camera.scale;
    ctx.beginPath();
    for (const e of content.edges) {
      const a = center(e.from),
        b = center(e.to);
      ctx.moveTo(a.x, a.y);
      ctx.lineTo(b.x, b.y);
    }
    ctx.stroke();
    for (const item of content.overview) {
      ctx.fillStyle = item.group
        ? '#726790'
        : item.node?.asset_id
          ? '#427a71'
          : item.node?.run_id
            ? '#75659e'
            : '#494555';
      ctx.fillRect(item.x, item.y, nodeWidth, nodeHeight);
    }
  }, [camera, content, size, visible]);
  function pointerStart(e: ReactPointerEvent<HTMLDivElement>) {
    if (!e.isPrimary || e.button !== 0) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const item = scene.hit(worldPoint(camera, e.clientX - rect.left, e.clientY - rect.top));
    const target = (e.target as HTMLElement).closest<HTMLElement>('.free-canvas-card');
    const exact =
      target?.dataset.nodeId || (target?.dataset.groupId ? '!' + target.dataset.groupId : undefined);
    const hit = exact ? scene.items.get(exact) : item;
    gesture.current = { id: e.pointerId, start: { x: e.clientX, y: e.clientY }, camera, item: hit };
    e.currentTarget.setPointerCapture(e.pointerId);
    if (hit) onSelect(hit.id);
    e.currentTarget.focus({ preventScroll: true });
  }
  function pointerMove(e: ReactPointerEvent<HTMLDivElement>, finish = false) {
    const g = gesture.current;
    if (!g || g.id !== e.pointerId) return;
    const dx = e.clientX - g.start.x,
      dy = e.clientY - g.start.y;
    if (g.item?.node && !locked) {
      const id = g.item.id,
        point = { x: coord(g.item.x + dx / g.camera.scale), y: coord(g.item.y + dy / g.camera.scale) };
      if (finish) {
        cancelFrame();
        setDrag(undefined);
        if (dx || dy) onMove(id, point);
      } else frameUpdate(() => setDrag({ id, point }));
    } else if (!g.item) {
      const next = { ...g.camera, x: g.camera.x + dx, y: g.camera.y + dy };
      if (finish) {
        cancelFrame();
        setCamera(next);
      } else frameUpdate(() => setCamera(next));
    }
    if (finish) {
      gesture.current = undefined;
      if (e.currentTarget.hasPointerCapture(e.pointerId)) e.currentTarget.releasePointerCapture(e.pointerId);
    }
  }
  function key(e: React.KeyboardEvent) {
    if (e.altKey || e.metaKey || e.ctrlKey) return;
    const d: Record<string, Point> = {
      ArrowLeft: { x: -1, y: 0 },
      ArrowRight: { x: 1, y: 0 },
      ArrowUp: { x: 0, y: -1 },
      ArrowDown: { x: 0, y: 1 },
    };
    if (d[e.key]) {
      e.preventDefault();
      const v = d[e.key],
        item = scene.items.get(selected);
      if (e.shiftKey && item?.node && !locked)
        onMove(item.id, { x: coord(item.x + v.x * 8), y: coord(item.y + v.y * 8) });
      else setCamera((c) => ({ ...c, x: c.x - v.x * 48, y: c.y - v.y * 48 }));
    } else if (['+', '=', '-'].includes(e.key)) {
      e.preventDefault();
      setCamera((c) =>
        zoomAt(c, { x: size.width / 2, y: size.height / 2 }, c.scale * (e.key === '-' ? 0.8 : 1.25)),
      );
    } else if (e.key === 'Home') {
      e.preventDefault();
      setCamera(fit(scene.box, size.width, size.height));
    }
  }
  return (
    <div className="free-canvas-stage">
      <div
        role="region"
        aria-label={uiText("自由画布视口")}
        tabIndex={0}
        className="free-canvas-viewport"
        ref={viewport}
        data-node-count={doc.nodes.length}
        data-edge-count={doc.edges.length}
        data-detailed-count={content.detailed.length}
        data-zoom={camera.scale}
        data-pan-x={camera.x}
        data-pan-y={camera.y}
        onKeyDown={key}
        onPointerDown={pointerStart}
        onPointerMove={(e) => pointerMove(e)}
        onPointerUp={(e) => pointerMove(e, true)}
        onPointerCancel={() => {
          cancelFrame();
          gesture.current = undefined;
          setDrag(undefined);
        }}
      >
        <canvas
          ref={raster}
          className="free-canvas-lines"
          aria-hidden="true"
          data-visible-edges={content.edges.length}
          data-overview-nodes={content.overview.length}
        />
        <div
          className="free-canvas-world"
          style={{
            transform: 'translate(' + camera.x + 'px,' + camera.y + 'px) scale(' + camera.scale + ')',
          }}
        >
          {content.detailed.map((item) => (
            <ItemCard
              key={item.id}
              item={item}
              selected={item.id === selected}
              project={project}
              revision={revision}
              source={sources[item.id]}
              choose={onSelect}
            />
          ))}
        </div>
        {!doc.nodes.length && <p className="free-canvas-empty">{uiText("添加一个节点，开始整理想法。")}</p>}
      </div>
      <div className="free-canvas-tools">
        <button
          type="button"
          aria-label={uiText("缩小")}
          onClick={() =>
            setCamera((c) => zoomAt(c, { x: size.width / 2, y: size.height / 2 }, c.scale * 0.8))
          }
        >
          −
        </button>
        <output aria-label={uiText("缩放比例")}>{Math.round(camera.scale * 100)}%</output>
        <button
          type="button"
          aria-label={uiText("放大")}
          onClick={() =>
            setCamera((c) => zoomAt(c, { x: size.width / 2, y: size.height / 2 }, c.scale * 1.25))
          }
        >
          ＋
        </button>
        <button type="button" onClick={() => setCamera(fit(scene.box, size.width, size.height))}>{uiText("适应全图")}</button>
      </div>
    </div>
  );
}
