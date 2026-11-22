import { useState } from 'react';
import { applyLanguage, isLocale, languageNames, useLanguage } from './locale';

const labels = {
  'zh-CN': ['界面语言', '自动', '语言包载入失败，请重新选择。'],
  'zh-TW': ['介面語言', '自動', '語言套件載入失敗，請重新選擇。'],
  en: ['Interface language', 'Auto', 'Could not load the language. Please choose again.'],
  ja: ['表示言語', '自動', '言語を読み込めませんでした。もう一度選択してください。'],
};

export function LanguagePicker() {
  const { locale, preference } = useLanguage();
  const [error, setError] = useState(false);
  const [label, automatic, failure] = labels[locale];
  return <label className="language-picker">
    <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
      <circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3 12h18"/>
    </svg>
    <select aria-label={label} title={label} value={preference} onChange={event => {
      const value = event.target.value;
      if (value !== 'auto' && !isLocale(value)) return;
      setError(false);
      void applyLanguage(value).catch(() => setError(true));
    }}>
      <option value="auto">{automatic} · {languageNames[locale]}</option>
      {Object.entries(languageNames).map(([code, name]) => <option key={code} value={code} lang={code}>{name}</option>)}
    </select>
    {error && <span role="alert">{failure}</span>}
  </label>;
}
