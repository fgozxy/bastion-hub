import axios from 'axios';

export type ContainerOperationIssue = {
  name: string;
  reason: string;
};

export type NormalizedContainerOperation = {
  ok: boolean;
  updated: string[];
  unchanged: string[];
  skipped: ContainerOperationIssue[];
  failed: ContainerOperationIssue[];
  error: string;
};

const asNames = (value: unknown): string[] =>
  Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string' && v.length > 0) : [];

const asIssues = (value: unknown): ContainerOperationIssue[] => {
  if (Array.isArray(value)) {
    return value.flatMap((item) => {
      if (typeof item === 'string') return [{ name: item, reason: '' }];
      if (!item || typeof item !== 'object') return [];
      const v = item as Record<string, unknown>;
      const name = String(v.name || v.container || v.id || '').trim();
      if (!name) return [];
      return [{ name, reason: String(v.reason || v.error || v.err || '').trim() }];
    });
  }
  if (value && typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>).map(([name, reason]) => ({
      name,
      reason: String(reason || '').trim(),
    }));
  }
  return [];
};

// New agents return structured updated/unchanged/skipped/failed fields. Keep
// parsing Details so rolling upgrades still report older agents accurately.
export function normalizeContainerOperation(value: any): NormalizedContainerOperation {
  if (!value || typeof value !== 'object') {
    return {
      ok: false,
      updated: [],
      unchanged: [],
      skipped: [],
      failed: [{ name: '请求', reason: '返回结果无效' }],
      error: '返回结果无效',
    };
  }
  const updated = asNames(value?.updated);
  const unchanged = asNames(value?.unchanged);
  const skipped = asIssues(value?.skipped);
  const failed = asIssues(value?.failed);
  const seen = new Set([
    ...updated,
    ...unchanged,
    ...skipped.map((v) => v.name),
    ...failed.map((v) => v.name),
  ]);

  if (value?.details && typeof value.details === 'object') {
    Object.entries(value.details as Record<string, unknown>).forEach(([name, raw]) => {
      const detail = String(raw || '').trim();
      const skippedIssue = skipped.find((v) => v.name === name);
      if (skippedIssue && !skippedIssue.reason && /skip/i.test(detail)) skippedIssue.reason = detail;
      if (seen.has(name)) return;
      if (/^(error|failed)\s*:/i.test(detail)) {
        failed.push({ name, reason: detail.replace(/^(error|failed)\s*:\s*/i, '') });
      } else if (/already up to date|unchanged|no change/i.test(detail)) {
        unchanged.push(name);
      } else if (/skip/i.test(detail)) {
        skipped.push({ name, reason: detail });
      } else if (detail) {
        // Legacy agents put successful action details here, but also list the
        // affected container in `updated`; do not infer success from prose.
      }
    });
  }

  const error = String(value?.err || value?.error || '').trim();
  if ((value?.ok === false || error) && failed.length === 0) {
    failed.push({ name: '请求', reason: error || 'Agent 返回失败' });
  }
  return {
    ok: value?.ok !== false && !error && failed.length === 0,
    updated,
    unchanged,
    skipped,
    failed,
    error,
  };
}

export const http = axios.create({
  baseURL: '',
  withCredentials: true,
  timeout: 30000,
});

http.interceptors.response.use(
  (r) => r,
  (err) => {
    if (err?.response?.status === 401 && !window.location.pathname.startsWith('/login')) {
      window.location.href = '/login';
    }
    return Promise.reject(err);
  }
);

async function get<T = any>(url: string, params?: any): Promise<T> {
  const r = await http.get(url, { params });
  return r.data;
}
async function post<T = any>(url: string, body?: any): Promise<T> {
  const r = await http.post(url, body);
  return r.data;
}
async function put<T = any>(url: string, body?: any): Promise<T> {
  const r = await http.put(url, body);
  return r.data;
}
async function patch<T = any>(url: string, body?: any): Promise<T> {
  const r = await http.patch(url, body);
  return r.data;
}
async function del<T = any>(url: string): Promise<T> {
  const r = await http.delete(url);
  return r.data;
}

export const api = {
  auth: {
    login: (username: string, password: string) => post('/api/auth/login', { username, password }),
    logout: () => post('/api/auth/logout'),
    me: () => get('/api/auth/me'),
  },
  dashboard: () => get('/api/dashboard'),
  nodes: {
    list: () => get('/api/nodes'),
    create: (name: string) => post('/api/nodes', { name }),
    rename: (id: string, name: string, ssh_port: string) => patch(`/api/nodes/${id}`, { name, ssh_port }),
    firewallStatus: (node_id: string) => post('/api/nodes/firewall/status', { node_id }),
    firewallToggle: (node_id: string, action: 'enable' | 'disable') =>
      post('/api/nodes/firewall/toggle', { node_id, action }),
    firewallPorts: (node_id: string, ports: string[], action: 'allow' | 'deny') =>
      post('/api/nodes/firewall/ports', { node_id, ports, action }),
    regenerate: (id: string) => post(`/api/nodes/${id}/regenerate`),
    updateAgent: (id: string) => post(`/api/nodes/${id}/update-agent`),
    updateAgents: (node_ids: string[]) => post('/api/nodes/update-agents', { node_ids }),
    remove: (id: string) => del(`/api/nodes/${id}`),
  },
  mesh: {
    access: () => get('/api/mesh/access'),
    putAccess: (config: { enabled: boolean; node_ids: string[]; source_cidrs: string[] }) =>
      put('/api/mesh/access', config),
  },
  health: {
    // Per-node status (online, netdata installed/enabled, agent capability, latest sample).
    status: () => get('/api/health'),
    // Batch-install Netdata (loopback-only) on the selected nodes.
    install: (node_ids: string[]) => post('/api/health/install', { node_ids }),
    // Batch-UNINSTALL Netdata from the selected nodes (frees its memory/CPU).
    uninstall: (node_ids: string[]) => post('/api/health/uninstall', { node_ids }),
    // Cached rolling history for one node's charts.
    metrics: (node_id: string, window = 180) => get('/api/health/metrics', { node_id, window }),
    // Per-node alert rules.
    alerts: (node_id: string) => get('/api/health/alerts', { node_id }),
    putAlert: (a: { id?: string; node_id: string; metric: string; threshold: number; window_sec: number; enabled: boolean }) =>
      put('/api/health/alerts', a),
    delAlert: (id: string) => del(`/api/health/alerts/${id}`),
    // Debug: run an HTTPFetch through the agent and return the raw result.
    testFetch: (node_id: string, url = '') => post('/api/health/test-fetch', { node_id, url }),
    // Metric template: which metrics to collect/render + default alert rules.
    getTemplate: () => get('/api/health/template'),
    putTemplate: (t: { enabled: string[]; alerts: any[] }) => put('/api/health/template', t),
    resetTemplate: () => post('/api/health/template/reset', {}),
  },
  commands: {
    run: (node_ids: string[], cmd: string, timeout = 0) =>
      post('/api/commands', { node_ids, cmd, timeout }),
    list: () => get('/api/commands'),
    get: (id: string) => get(`/api/commands/${id}`),
    saved: {
      list: () => get('/api/commands/saved'),
      create: (name: string, script: string) => post('/api/commands/saved', { name, script }),
      remove: (id: string) => del(`/api/commands/saved/${id}`),
    },
  },
  credentials: {
    list: () => get('/api/credentials'),
    create: (c: any) => post('/api/credentials', c),
    bind: (id: string, node_id: string) => post(`/api/credentials/${id}/bind`, { node_id }),
    test: (id: string) => post(`/api/credentials/${id}/test`, {}),
    remove: (id: string) => del(`/api/credentials/${id}`),
    scan: (nodeID: string) => post(`/api/credentials/scan/${nodeID}`),
    scanMulti: (node_ids: string[]) => post('/api/credentials/scan-multi', { node_ids }),
    importKeys: (node_id: string, keys: any[]) => post('/api/credentials/import', { node_id, keys }),
  },
  backups: {
    list: (node_id?: string) => get('/api/backups', { node_id }),
    now: (b: any) => post('/api/backups/now', b),
    restore: (id: string, node_id: string, dest: string) =>
      post(`/api/backups/${id}/restore`, { node_id, dest }),
    remove: (id: string) => del(`/api/backups/${id}`),
  },
  restore: {
    // Deduplicated container-restore view: newest/most-complete backup per
    // container, merged across nodes.
    list: () => get('/api/backups/containers/restore'),
    // Restore the selected backups to one or more target nodes (cross-node DR),
    // recreating the containers there. auto_pull: pull a missing image before recreate.
    toNode: (node_ids: string[], backup_ids: string[], auto_pull = false) =>
      post('/api/backups/containers/restore', { node_ids, backup_ids, auto_pull }),
    // All snapshots of one container (newest first) — for the expandable history row.
    snapshots: (name: string) => get('/api/restore/container-backups', { name }),
    // Persisted restore jobs (history).
    jobs: () => get('/api/restore/jobs'),
    // Pre-restore feasibility check (port/path conflicts, image, disk) per target node.
    preflight: (backup_ids: string[], node_ids: string[]) =>
      post('/api/restore/preflight', { backup_ids, node_ids }),
  },
  targets: {
    list: () => get('/api/targets'),
    create: (t: any) => post('/api/targets', t),
    update: (id: string, t: any) => put(`/api/targets/${id}`, t),
    remove: (id: string) => del(`/api/targets/${id}`),
    test: (id: string) => post(`/api/targets/${id}/test`),
    githubResolve: (token: string, repo: string) => post('/api/targets/github/resolve', { token, repo }),
    githubRepos: (token: string) => post('/api/targets/github/repos', { token }),
    vpsList: (cfg: any, path: string) => post('/api/targets/vps/list', { ...cfg, path }),
    onedriveDevice: (client_id: string) => post('/api/targets/onedrive/device', { client_id }),
    onedrivePoll: (client_id: string, device_code: string) =>
      post('/api/targets/onedrive/poll', { client_id, device_code }),
  },
  schedules: {
    list: () => get('/api/schedules'),
    create: (s: any) => post('/api/schedules', s),
    update: (id: string, s: any) => put(`/api/schedules/${id}`, s),
    remove: (id: string) => del(`/api/schedules/${id}`),
  },
  settings: {
    all: () => get('/api/settings'),
    putAccount: (username: string, new_password: string) =>
      put('/api/settings/account', { username, new_password }),
    putTelegram: (bot_token: string, chat_id: string) =>
      put('/api/settings/telegram', { bot_token, chat_id }),
    testTelegram: (bot_token: string, chat_id: string) =>
      post('/api/settings/telegram/test', { bot_token, chat_id }),
    putRetention: (keep_count: number, keep_days: number) =>
      put('/api/settings/retention', { keep_count, keep_days }),
    putExcludes: (excludes: string[]) =>
      put('/api/settings/excludes', { excludes }),
    putContainerMonitor: (enabled: boolean, interval_seconds: number) =>
      put('/api/settings/container-monitor', { enabled, interval_seconds }),
    putGithub: (token: string, owner: string, repo: string, branch: string, force: boolean) =>
      put('/api/settings/github', { token, owner, repo, branch, force }),
    githubPushProject: (token: string, owner: string, repo: string, branch: string, force: boolean) =>
      post('/api/settings/github/push-project', { token, owner, repo, branch, force }),
  },
  container: {
    update: (node_id: string, label = '') => post('/api/container/update', { node_id, label }),
  },
  containers: {
    list: () => get('/api/containers'),
    // Update/upgrade/rebuild runs compose pull + recreate, which can take minutes
    // per node. The server dispatches with a 10-minute budget, so give the request
    // enough room to receive the real result instead of hitting the global 30s cap
    // (which would abort the browser request while the server still succeeds).
    action: async (node_id: string, ids: string[], action: string, label = '', new_image = '') => {
      const r = await http.post(
        '/api/containers/action',
        { node_id, ids, action, label, new_image },
        { timeout: 620000 },
      );
      return r.data;
    },
    // The server waits up to 90 seconds per node (in parallel). Give it enough
    // room to return the coverage report instead of hitting the global 30s cap.
    scanUpdates: async () => {
      const r = await http.post('/api/containers/scan-updates', {}, { timeout: 100000 });
      return r.data;
    },
    setName: (node_id: string, name: string, display_name: string) =>
      put('/api/containers/name', { node_id, name, display_name }),
  },
};
