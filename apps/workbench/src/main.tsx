import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import { takeAccountLink } from './accountSecurity';
import './styles.css';
import './auth.css';
import './drafts.css';
import './runtime.css';
import './models.css';
import './references.css';

const initialAccountLink = takeAccountLink(window.location, window.history);
createRoot(document.getElementById('root')!).render(<StrictMode><App initialAccountLink={initialAccountLink}/></StrictMode>);

import './library.css';
