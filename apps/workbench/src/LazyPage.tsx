import { uiText } from './locale';
import { Component, Suspense, lazy, useMemo, useState } from 'react';
import type { ComponentType, ReactNode } from 'react';
class PageBoundary extends Component<{ children: ReactNode; retry: () => void }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  render() { return this.state.failed ? <section className="page-loading" role="alert"><h2>{uiText("此页面暂未载入")}</h2><p>{uiText("当前页面内存中的待确认记录仍保留。请重试加载；不要刷新整页，以免丢失未提交输入。")}</p><button className="button secondary" onClick={this.props.retry}>{uiText("重试载入页面")}</button></section> : this.props.children; }
}
export function LazyPage<P extends object>({ load, pageProps }: { load: () => Promise<{ default: ComponentType<P> }>; pageProps: P }) {
  const [version, retry] = useState(0), Page = useMemo(() => lazy(load), [load, version]);
  return <PageBoundary key={version} retry={() => retry(value => value + 1)}><Suspense fallback={<section className="page-loading" role="status">{uiText("正在载入工作区页面…")}</section>}><Page {...pageProps}/></Suspense></PageBoundary>;
}
