import { uiText, currentLocale } from './locale';
import { useEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import { downloadDataPage, exportSections, requestDataPage } from './accountDataExport';
import type { ExportRange, ExportSection } from './accountDataExport';
import './account-data-export.css';

type Props = { owner: string; restricted: boolean; onExpired: () => void };
type Delivery = {
  page: number;
  count: number;
  cutoff: string;
  readAt: string;
  next: string | null;
  digest: string;
};
const fmt = (s: string) => new Date(s).toLocaleString(currentLocale(), { hour12: false });
export function AccountDataExport({ owner, restricted, onExpired }: Props) {
  const [section, setSection] = useState<ExportSection>('profile');
  const [range, setRange] = useState<ExportRange>({ page: 1 });
  const [result, setResult] = useState<Delivery | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const password = useRef<HTMLInputElement>(null),
    pending = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      pending.current?.abort();
      pending.current = null;
    },
    [],
  );
  function setPage(next: ExportRange) {
    if (password.current) password.current.value = '';
    setRange(next);
    setResult(null);
    setError('');
  }
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (pending.current || !password.current) return;
    const secret = password.current.value;
    password.current.value = '';
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true);
    setError('');
    setResult(null);
    const timeout = window.setTimeout(() => controller.abort(), 30000);
    try {
      const value = await requestDataPage({
        owner,
        section,
        range,
        password: secret,
        signal: controller.signal,
      });
      if (pending.current !== controller || controller.signal.aborted) return;
      downloadDataPage(value.blob, section, range.page);
      setResult({
        page: range.page,
        count: value.count,
        readAt: value.readAt,
        cutoff: value.cutoff,
        next: value.next,
        digest: value.digest,
      });
    } catch (cause) {
      if (pending.current !== controller) return;
      if (cause instanceof ApiError && cause.status === 401) {
        onExpired();
        return;
      }
      setError(cause instanceof Error ? cause.message : uiText('资料下载未完成，请重新输入密码后手动提交。'));
    } finally {
      window.clearTimeout(timeout);
      if (pending.current === controller) {
        pending.current = null;
        setBusy(false);
        if (password.current) password.current.value = '';
      }
    }
  }
  return (
    <section
      className="security-card account-export"
      aria-labelledby="account-export-heading"
      data-export-section={section}
      data-export-page={range.page}
    >
      <header>
        <h2 id="account-export-heading">{uiText("下载本人账户资料")}</h2>
        <p>
          {restricted
            ? uiText('当前受限会话可下载本人的账户资料、安全事件和账户申诉。')
            : uiText('选择一个资料范围，用当前密码下载这一页的 JSON 文件。')}{' '}{uiText("每页最多 100 条，仅包含当前账户的记录。")}</p>
      </header>
      <form onSubmit={(e) => void submit(e)} autoComplete="off">
        <label htmlFor="account-export-section">{uiText("资料范围")}</label>
        <select
          id="account-export-section"
          value={section}
          disabled={busy}
          onChange={(e) => {
            setSection(e.target.value as ExportSection);
            setPage({ page: 1 });
          }}
        >
          {Object.entries(exportSections).map(([id, s]) => (
            <option key={id} value={id}>
              {s.name}
            </option>
          ))}
        </select>
        <div className="account-export-range">
          <strong>
            {exportSections[section].name} {uiText("· 第")} {range.page} {uiText("页")}</strong>
          <p>
            {exportSections[section].scope}。
            {section === 'profile'
              ? uiText('此范围只有一页。')
              : uiText('按创建时间由近到远排列。续页沿用首页的创建截止时间，内容按各页读取时的状态提供。')}
          </p>
          {range.cutoff && <small>{uiText("本次创建截止时间")} {fmt(range.cutoff)}</small>}
        </div>
        <label htmlFor="account-export-password">{uiText("下载验证用的当前密码")}</label>
        <input
          id="account-export-password"
          type="password"
          autoComplete="off"
          required
          maxLength={72}
          disabled={busy}
          ref={password}
        />
        <div className="account-export-actions">
          <button className="button primary" type="submit" disabled={busy}>
            {busy ? uiText('正在验证并准备文件…') : uiText('验证密码并下载本页')}
          </button>
          {range.page > 1 && (
            <button
              type="button"
              className="button secondary"
              disabled={busy}
              onClick={() => setPage({ page: 1 })}
            >{uiText("从第一页重新开始")}</button>
          )}
        </div>
      </form>
      {error && (
        <p className="account-export-error" role="alert">
          {error}
        </p>
      )}
      {result && (
        <div
          className="account-export-result"
          role="status"
          data-export-count={result.count}
          data-export-sha={result.digest}
        >
          <strong>{uiText("第")} {result.page} {uiText("页已交给浏览器下载 ·")} {result.count} {uiText("条")}</strong>
          <small>{uiText("读取时间")} {fmt(result.readAt)}</small>
          <small>{uiText("文件 SHA-256")} <code>{result.digest}</code>
          </small>
          {result.next ? (
            <button
              type="button"
              className="button secondary"
              onClick={() => setPage({ page: range.page + 1, cursor: result.next!, cutoff: result.cutoff })}
            >{uiText("准备下一页")}</button>
          ) : (
            <span>{uiText("本范围已到最后一页。")}</span>
          )}
        </div>
      )}
    </section>
  );
}
