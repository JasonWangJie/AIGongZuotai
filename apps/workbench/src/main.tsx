import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import { takeAccountLink } from './accountSecurity';
import { savedPreference, applyLanguage } from './locale';
import './styles.css';
import './auth.css';
import './drafts.css';
import './runtime.css';
import './models.css';
import './references.css';
import './library.css';
import './language.css';

const initialAccountLink = takeAccountLink(window.location, window.history);
void applyLanguage(savedPreference(), false)
  .catch(() => applyLanguage('zh-CN', false))
  .then(() => createRoot(document.getElementById('root')!).render(<StrictMode><App initialAccountLink={initialAccountLink}/></StrictMode>));
