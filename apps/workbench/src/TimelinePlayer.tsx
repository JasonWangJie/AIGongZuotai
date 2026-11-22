import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import type { Clip } from './videoTimeline';
export function TimelinePlayer({
  clip,
  src,
  auto,
  advance,
  loaded,
}: {
  clip: Clip;
  src: string;
  auto: boolean;
  advance: () => void;
  loaded: (ms: number) => void;
}) {
  const player = useRef<HTMLVideoElement>(null),
    bounded = useRef(false),
    [issue, setIssue] = useState(''),
    [ms, setMs] = useState(0);
  const latest = useRef({ clip, auto, advance, loaded });
  latest.current = { clip, auto, advance, loaded };
  useEffect(() => {
    bounded.current = false;
    setIssue('');
    setMs(0);
    const video = player.current;
    return () => video?.pause();
  }, [src]);
  function seek(value: number) {
    const v = player.current;
    if (!v || !Number.isFinite(value) || !Number.isFinite(v.duration)) return;
    v.currentTime = Math.max(0, Math.min(value / 1000, v.duration));
  }
  async function play() {
    const v = player.current,
      c = latest.current.clip;
    if (
      !v ||
      !Number.isFinite(v.duration) ||
      c.in_ms < 0 ||
      c.out_ms <= c.in_ms ||
      c.out_ms > Math.round(v.duration * 1000)
    ) {
      setIssue(uiText('裁点超出当前片源的可播放范围。'));
      return;
    }
    setIssue('');
    seek(c.in_ms);
    bounded.current = true;
    try {
      await v.play();
    } catch {
      bounded.current = false;
      setIssue(uiText('播放器未能开始播放，可使用原片控件重试。'));
    }
  }
  function ended() {
    if (!bounded.current) return;
    bounded.current = false;
    player.current?.pause();
    if (latest.current.auto) latest.current.advance();
  }
  useEffect(() => {
    let frame = 0;
    const v = player.current;
    if (!v) return;
    const tick = () => {
      if (bounded.current && v.currentTime * 1000 >= latest.current.clip.out_ms - 12) ended();
      if (!v.paused) frame = requestAnimationFrame(tick);
    };
    function onPlay() {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(tick);
    }
    v.addEventListener('play', onPlay);
    return () => {
      cancelAnimationFrame(frame);
      v.removeEventListener('play', onPlay);
    };
  }, [src]);
  useEffect(() => {
    if (auto && ms) void play();
    else if (!auto) {
      bounded.current = false;
      player.current?.pause();
    }
  }, [auto, ms, clip.id]);
  return (
    <section className="vt-monitor" aria-label={uiText("镜头播放器")}>
      <div className="vt-screen">
        {src ? (
          <video
            key={src}
            ref={player}
            src={src}
            controls
            playsInline
            preload="metadata"
            onLoadedMetadata={(e) => {
              const v = e.currentTarget;
              const duration = Math.round(v.duration * 1000);
              if (!Number.isSafeInteger(duration) || duration <= 0) {
                setIssue(uiText('无法读取原片时长。'));
                return;
              }
              setMs(duration);
              latest.current.loaded(duration);
              seek(latest.current.clip.in_ms);
            }}
            onEnded={ended}
            onError={() => setIssue(uiText('当前原片不能播放，请核对项目权限和素材状态。'))}
          />
        ) : (
          <p>{uiText("原片不可用")}</p>
        )}
      </div>
      <div className="vt-monitor-bar">
        <span>
          {ms
            ? uiText('片源 ') + (ms / 1000).toFixed(3) + uiText(' 秒')
            : src
              ? uiText('等待读取片源时长')
              : uiText('当前版本的素材已不可读')}
        </span>
        <button
          disabled={!ms || !!auto}
          onClick={() => {
            bounded.current = false;
            player.current?.pause();
            seek(clip.in_ms);
          }}
        >{uiText("定位入点")}</button>
        <button
          disabled={!ms || !!auto}
          onClick={() => {
            bounded.current = false;
            player.current?.pause();
            seek(clip.out_ms);
          }}
        >{uiText("定位出点")}</button>
        <button disabled={!ms || auto} onClick={() => void play()}>{uiText("预览此镜头")}</button>
      </div>
      {issue && <p role="alert">{issue}</p>}
    </section>
  );
}
