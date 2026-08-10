import { create } from 'zustand';
import { api } from '../services/api';

export type Theme = 'light' | 'dark' | 'white';

interface AuthState {
  authed: boolean | null;
  username: string;
  check: () => Promise<void>;
  login: (u: string, p: string) => Promise<boolean>;
  logout: () => Promise<void>;
}

export const useAuth = create<AuthState>((set) => ({
  authed: null,
  username: '',
  check: async () => {
    try {
      const r = await api.auth.me();
      set({ authed: !!r.authenticated, username: r.username || '' });
    } catch {
      set({ authed: false });
    }
  },
  login: async (u, p) => {
    try {
      await api.auth.login(u, p);
      set({ authed: true, username: u });
      return true;
    } catch {
      return false;
    }
  },
  logout: async () => {
    try {
      await api.auth.logout();
    } catch {
      /* ignore */
    }
    set({ authed: false, username: '' });
  },
}));

interface ThemeState {
  theme: Theme;
  setTheme: (t: Theme) => void;
  init: () => void;
}

export const useTheme = create<ThemeState>((set, get) => ({
  theme: 'light',
  setTheme: (t) => {
    localStorage.setItem('np_theme', t);
    document.documentElement.setAttribute('data-theme', t);
    set({ theme: t });
  },
  init: () => {
    const t = (localStorage.getItem('np_theme') as Theme) || 'light';
    get().setTheme(t);
  },
}));

interface Toast {
  id: number;
  msg: string;
  type: string;
}
interface NotifyState {
  toasts: Toast[];
  push: (msg: string, type?: string) => void;
  dismiss: (id: number) => void;
}

let tid = 0;
export const useNotify = create<NotifyState>((set, get) => ({
  toasts: [],
  push: (msg, type = 'info') => {
    const id = ++tid;
    set((s) => ({ toasts: [...s.toasts, { id, msg, type }] }));
    setTimeout(() => get().dismiss(id), 3500);
  },
  dismiss: (id) => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}));

export const notify = (msg: string, type?: string) => useNotify.getState().push(msg, type);
