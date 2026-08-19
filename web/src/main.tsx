import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import { useTheme } from './stores';
import './styles/global.scss';

// Initialise the theme once before first render (never call setTheme during render).
useTheme.getState().init();

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
