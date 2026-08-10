import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import { useTheme } from './stores';
import './styles/global.scss';

// Initialise the theme once before first render (never call setTheme during render).
useTheme.getState().init();

// Capacitor / installed PWA shell: tweak layout (safe areas already in CSS) and
// hide "download APK" prompts that only make sense in a regular browser.
const native =
  !!(window as any).Capacitor ||
  window.matchMedia('(display-mode: standalone)').matches ||
  (navigator as any).standalone === true;
if (native) document.body.classList.add('is-native-shell');

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
