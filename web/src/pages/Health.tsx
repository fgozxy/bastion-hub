import { useEffect, useState } from 'react';
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  ArcElement,
  Tooltip,
  Legend,
  Filler,
} from 'chart.js';
import { Line, Doughnut } from 'react-chartjs-2';
import { Activity, RefreshCw, Settings2, Trash2, Plus, AlertTriangle, LayoutTemplate, RotateCcw, Pencil, CheckCheck, Square, Search, Download } from 'lucide-react';
import { api } from '../services/api';
import { notify } from '../stores';
import { Modal, Empty, StatusBadge, ConfirmModal } from '../components/ui';

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, ArcElement, Tooltip, Legend, Filler);

const accent = '#8b8680';
const success = '#10b981';
const warning = '#c65746';
const muted = '#a29c95';

// Metrics selectable for an alert rule (catalog keys + legacy per-load ones).
const ALERT_METRICS = [
  { key: 'cpu', label: 'CPU', unit: '%' },
  { key: 'mem', label: '内存', unit: '%' },
  { key: 'swap', label: 'Swap', unit: '%' },
  { key: 'disk', label: '磁盘空间', unit: '%' },
  { key: 'iowait', label: 'I/O 等待', unit: '%' },
  { key: 'load', label: '1分钟平均负载（0=核心数×2）', unit: '' },
  { key: 'load1', label: '1分钟平均负载', unit: '' },
  { key: 'load5', label: '5分钟平均负载', unit: '' },
  { key: 'load15', label: '15分钟平均负载', unit: '' },
];
const alertMetricLabel = (k: string) => ALERT_METRICS.find((m) => m.key === k)?.label || k;
const alertMetricUnit = (k: string) => ALERT_METRICS.find((m) => m.key === k)?.unit || '';

const fmt = (v: number | undefined, digits = 2) =>
  v == null ? '—' : v === Math.round(v) ? String(v) : v.toFixed(digits);

type Tmpl = { enabled: string[]; alerts: any[] };

export function HealthPage() {
  const [nodes, setNodes] = useState<any[]>([]);
  const [hist, setHist] = useState<Record<string, any[]>>({});
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [alertNode, setAlertNode] = useState<any | null>(null);
  const [tmpl, setTmpl] = useState<Tmpl | null>(null);
  const [tmplOpen, setTmplOpen] = useState(false);
  const [mgrOpen, setMgrOpen] = useState(false);
  const [pendingNode, setPendingNode] = useState<any | null>(null);
  // Per-node failures from the last batch install/uninstall, shown in a modal —
  // the toast auto-dismisses in 3.5s, far too fast to read a script error.
  const [failures, setFailures] = useState<{ title: string; items: { name: string; err: string }[] } | null>(null);

  const load = async () => {
    try {
      const st: any = await api.health.status();
      const arr = Array.isArray(st) ? st : [];
      setNodes(arr);
      const installed = arr.filter((n: any) => n.installed && n.online);
      const results = await Promise.all(
        installed.map((n: any) =>
          api.health.metrics(n.node_id, 300).then((h: any) => [n.node_id, Array.isArray(h) ? h : []] as [string, any[]])
        )
      );
      const m: Record<string, any[]> = {};
      results.forEach(([id, h]) => { m[id] = h; });
      setHist(m);
    } catch {
      /* ignore transient poll errors */
    } finally {
      setLoading(false);
    }
  };

  const loadTmpl = async () => {
    try {
      const r: any = await api.health.getTemplate();
      setTmpl(r?.template || null);
    } catch { /* ignore */ }
  };

  useEffect(() => {
    load();
    loadTmpl();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, []);

  const runInstall = async (ids: string[]) => {
    if (ids.length === 0) return notify('请选择节点', 'error');
    setBusy(true);
    try {
      const r: any = await api.health.install(ids);
      const arr: any[] = Array.isArray(r) ? r : [];
      const ok = arr.filter((x) => x.ok).length;
      notify(`完成：${ok}/${ids.length} 个节点已安装 Netdata`, ok === ids.length ? 'success' : 'error');
      showFailures('安装失败详情', arr);
      await load();
    } catch (e: any) {
      notify(e?.response?.data?.error || '安装失败', 'error');
    } finally {
      setBusy(false);
    }
  };

  const runUninstall = async (ids: string[]) => {
    if (ids.length === 0) return notify('请选择节点', 'error');
    setBusy(true);
    try {
      const r: any = await api.health.uninstall(ids);
      const arr: any[] = Array.isArray(r) ? r : [];
      const ok = arr.filter((x) => x.ok).length;
      notify(`完成：${ok}/${ids.length} 个节点已卸载 Netdata`, ok === ids.length ? 'success' : 'error');
      showFailures('卸载失败详情', arr);
      await load();
    } catch (e: any) {
      notify(e?.response?.data?.error || '卸载失败', 'error');
    } finally {
      setBusy(false);
    }
  };

  // The API returns per-node {ok, err} but the toast only shows the count —
  // surface the failures in a modal that stays open until dismissed.
  const showFailures = (title: string, arr: any[]) => {
    const failed = arr.filter((x) => !x.ok);
    if (failed.length === 0) return;
    setFailures({ title, items: failed.map((x) => ({ name: x.name || x.node_id, err: x.err || '未知错误' })) });
  };

  // Uninstall straight from a card (one node) — opens a centered ConfirmModal
  // (project-styled, not window.confirm) since it stops monitoring and removes
  // Netdata from the host.
  const confirmUninstallNode = (n: any) => setPendingNode(n);

  const installed = nodes.filter((n: any) => n.installed);
  const enabledSet = new Set(tmpl?.enabled || []);
  const installedCount = installed.length;
  const notInstalledCount = nodes.length - installedCount;

  return (
    <div>
      <div className="spread" style={{ marginBottom: 16 }}>
        <h1 className="page-title">
          <Activity size={22} style={{ verticalAlign: -4, marginRight: 6 }} />
          健康监控
        </h1>
        <div className="page-actions">
          <button className="btn primary" onClick={() => setMgrOpen(true)} disabled={busy}>
            <Download size={15} /> 安装 / 卸载
          </button>
          <button className="btn ghost" onClick={() => { setTmplOpen(true); }}>
            <LayoutTemplate size={15} /> 监控模板
          </button>
          <button className="btn ghost" onClick={load}>
            <RefreshCw size={15} /> 刷新
          </button>
        </div>
      </div>

      <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 16 }}>
        后端为各节点本地 Netdata（仅 <code>127.0.0.1:19999</code>），由 agent 拉取，15 秒一更。默认模板含 CPU / 平均负载 / 内存 / Swap / 磁盘空间 / 磁盘 I/O / 网络 / I/O 等待 / 进程，并预置 CPU/内存/磁盘/平均负载 默认告警。点右上「安装 / 卸载」批量给节点装/删监控（低配节点可随时关掉省内存）。
      </div>

      {loading ? (
        <Empty text="加载中…" />
      ) : installed.length === 0 ? (
        <Empty text="还没有节点安装 Netdata。点右上「安装 / 卸载」批量安装。" />
      ) : (
        <div className="auto-grid">
          {installed.map((n: any) => (
            <NodeCard key={n.node_id} node={n} history={hist[n.node_id] || []} enabled={enabledSet} onConfig={() => setAlertNode(n)} onUninstall={() => confirmUninstallNode(n)} busy={busy} />
          ))}
        </div>
      )}

      {notInstalledCount > 0 && (
        <div className="card" style={{ marginTop: 20, padding: 16 }}>
          <div className="spread" style={{ marginBottom: 8 }}>
            <span style={{ fontWeight: 600 }}>未安装 Netdata 的节点（{notInstalledCount}）</span>
            <button className="btn sm primary" onClick={() => setMgrOpen(true)}>
              <Download size={14} /> 去安装
            </button>
          </div>
          <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
            {nodes.filter((n: any) => !n.installed).map((n: any) => (
              <span key={n.node_id} className="badge muted">
                {n.name} {n.online ? '' : '· 离线'}
              </span>
            ))}
          </div>
        </div>
      )}

      {alertNode && <AlertModal node={alertNode} onClose={() => setAlertNode(null)} changed={load} />}
      {tmplOpen && (
        <TemplateModal
          onClose={() => setTmplOpen(false)}
          current={tmpl}
          changed={(t) => { setTmpl(t); load(); }}
        />
      )}
      {mgrOpen && (
        <ManageHealthModal
          nodes={nodes}
          installedCount={installedCount}
          onClose={() => setMgrOpen(false)}
          onInstall={async (ids) => { await runInstall(ids); }}
          onUninstall={async (ids) => { await runUninstall(ids); }}
        />
      )}
      {pendingNode && (
        <ConfirmModal
          title="卸载 Netdata"
          message={`确认从「${pendingNode.name}」卸载 Netdata？\n\n将停止该节点监控并删除 Netdata（释放其内存/CPU）。节点的告警规则会保留，下次重装自动恢复。`}
          confirmText="卸载"
          danger
          busy={busy}
          onClose={() => setPendingNode(null)}
          onConfirm={() => { const id = pendingNode.node_id; setPendingNode(null); runUninstall([id]); }}
        />
      )}
      {failures && (
        <Modal title={failures.title} wide onClose={() => setFailures(null)}>
          <div style={{ fontSize: 13, color: 'var(--text-secondary)', whiteSpace: 'pre-wrap', lineHeight: 1.6 }}>
            {failures.items.map((f) => `「${f.name}」\n${f.err}`).join('\n\n')}
          </div>
        </Modal>
      )}
    </div>
  );
}

// Catalog order so card layout is stable regardless of enabled ordering.
const CARD_ORDER = ['cpu', 'load', 'mem', 'swap', 'disk_space', 'disk_io', 'net', 'iowait', 'processes'];

function NodeCard({ node, history, enabled, onConfig, onUninstall, busy }: { node: any; history: any[]; enabled: Set<string>; onConfig: () => void; onUninstall: () => void; busy: boolean }) {
  const s: any = node.sample || {};
  const tsLabels = history.map((h: any) => new Date(h.ts * 1000).toLocaleTimeString());
  const oldAgent = !node.supports_http_fetch;

  const lineOpts = { maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: { x: { display: false }, y: { beginAtZero: true } } } as any;
  const doughnutOpts = { maintainAspectRatio: false, cutout: '68%', plugins: { legend: { display: false } } } as any;

  const iowaitHot = (s.iowait || 0) >= 20;

  // Build the chart cells dynamically from the enabled metrics, in catalog order.
  const cells: { label: string; el: any }[] = [];
  const on = (k: string) => enabled.size === 0 || enabled.has(k); // empty → show all (pre-template safety)

  if (on('load')) cells.push({ label: `平均负载 1/5/15 · ${fmt(s.load1)} / ${fmt(s.load5)} / ${fmt(s.load15)}`, el: line('load1', accent) });
  if (on('cpu')) cells.push({ label: `CPU ${fmt(s.cpu)}%`, el: line('cpu', success) });
  if (on('mem')) cells.push({ label: `内存 ${fmt(s.mem_used_pct)}%`, el: gauge(s.mem_used_pct, warning) });
  if (on('swap') && (s.swap_used_pct || 0) > 0) cells.push({ label: `Swap ${fmt(s.swap_used_pct)}%`, el: gauge(s.swap_used_pct, s.swap_used_pct > 70 ? warning : muted) });
  if (on('disk_space')) cells.push({ label: `磁盘 ${fmt(s.disk_used_pct)}%`, el: gauge(s.disk_used_pct, s.disk_used_pct > 85 ? warning : muted) });
  if (on('disk_io')) cells.push({ label: `磁盘 I/O 读 ${fmtBytes(s.disk_read)}/s · 写 ${fmtBytes(s.disk_write)}/s`, el: line('disk_read', '#6b8cff') });
  if (on('net')) cells.push({ label: `网络 ↓${fmtBytes(s.net_rx)}/s ↑${fmtBytes(s.net_tx)}/s`, el: line('net_rx', '#9b7bff') });

  // sparkline/text extras
  const extras: any[] = [];
  if (on('iowait')) extras.push(<span key="iw" style={{ color: iowaitHot ? warning : undefined }}><AlertTriangle size={12} style={{ verticalAlign: -2 }} /> I/O 等待 {fmt(s.iowait)}%</span>);
  if (on('processes')) extras.push(<span key="proc">进程 {fmt(s.proc_running, 0)} 跑 / {fmt(s.proc_blocked, 0)} 阻</span>);
  if (s.cores) extras.push(<span key="cores">{s.cores} 核</span>);

  function line(field: string, color: string) {
    return (
      <Line
        data={{ labels: tsLabels, datasets: [{ data: history.map((h: any) => h[field] || 0), borderColor: color, backgroundColor: color + '22', fill: true, tension: 0.35, pointRadius: 0 }] }}
        options={lineOpts}
      />
    );
  }
  function gauge(pct: number, color: string) {
    return (
      <Doughnut
        data={{ datasets: [{ data: [pct, Math.max(0, 100 - pct)], backgroundColor: [color, '#3a3633'], borderWidth: 0 }] }}
        options={doughnutOpts}
      />
    );
  }

  return (
    <div className="card" style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 12, minWidth: 0 }}>
      <div className="spread">
        <div className="row" style={{ gap: 8, alignItems: 'center' }}>
          <strong>{node.name}</strong>
          <StatusBadge online={node.online} />
          {oldAgent && <span className="badge warning" title="Agent 旧版，用 curl 回退拉取（建议升级 Agent 到 2.1.0+）">Agent 旧版</span>}
        </div>
        <div className="card-actions" style={{ marginTop: 0, width: 'auto', display: 'flex', gap: 4 }}>
          <button className="icon-btn" title="卸载 Netdata（停止监控并释放节点内存）" onClick={onUninstall} disabled={busy}>
            <Trash2 size={14} />
          </button>
          <button className="btn sm" onClick={onConfig} title="配置告警阈值" disabled={busy}>
            <Settings2 size={14} /> 告警
          </button>
        </div>
      </div>

      {!node.sample ? (
        <div style={{ color: 'var(--text-tertiary)', fontSize: 13, padding: '12px 0' }}>
          等待首个指标采样（≤15 秒）…若持续无数据，检查节点上 Netdata 是否运行。
        </div>
      ) : cells.length === 0 ? (
        <div style={{ color: 'var(--text-tertiary)', fontSize: 13, padding: '12px 0' }}>模板未启用任何可展示指标，到「监控模板」开启。</div>
      ) : (
        <>
          <div className="grid-2">
            {cells.map((c, i) => (
              <ChartBox key={i} label={c.label}>{c.el}</ChartBox>
            ))}
          </div>
          {extras.length > 0 && (
            <div className="row" style={{ gap: 12, fontSize: 12, color: 'var(--text-tertiary)' }}>{extras}</div>
          )}
        </>
      )}
    </div>
  );
}

function ChartBox({ label, children }: { label: string; children: any }) {
  return (
    <div style={{ minWidth: 0 }}>
      <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 4, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{label}</div>
      <div style={{ height: 96 }}>{children}</div>
    </div>
  );
}

function AlertModal({ node, onClose, changed }: { node: any; onClose: () => void; changed: () => void }) {
  const [alerts, setAlerts] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [metric, setMetric] = useState('cpu');
  const [threshold, setThreshold] = useState('90');
  const [windowSec, setWindowSec] = useState('60');
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<any | null>(null);

  const load = async () => {
    setLoading(true);
    try {
      const r: any = await api.health.alerts(node.node_id);
      setAlerts(Array.isArray(r) ? r : []);
    } catch { /* ignore */ } finally { setLoading(false); }
  };
  useEffect(() => { load(); }, []);

  const add = async () => {
    const t = parseFloat(threshold);
    const w = parseInt(windowSec, 10) || 60;
    if (isNaN(t)) return notify('阈值无效', 'error');
    setBusy(true);
    try {
      await api.health.putAlert({ node_id: node.node_id, metric, threshold: t, window_sec: w, enabled: true });
      notify('已添加告警规则', 'success');
      load();
      changed();
    } catch (e: any) {
      notify(e?.response?.data?.error || '添加失败', 'error');
    } finally { setBusy(false); }
  };

  const remove = async (id: string) => {
    try {
      await api.health.delAlert(id);
      load();
      changed();
    } catch (e: any) {
      notify(e?.response?.data?.error || '删除失败', 'error');
    }
  };

  const saveEdit = async () => {
    if (!editing) return;
    const t = parseFloat(editing.threshold);
    const w = parseInt(editing.window_sec, 10) || 60;
    if (isNaN(t)) return notify('阈值无效', 'error');
    setBusy(true);
    try {
      await api.health.putAlert({
        id: editing.id,
        node_id: node.node_id,
        metric: editing.metric,
        threshold: t,
        window_sec: w,
        enabled: editing.enabled,
      });
      notify('已更新告警规则', 'success');
      setEditing(null);
      load();
      changed();
    } catch (e: any) {
      notify(e?.response?.data?.error || '保存失败', 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={`配置告警 · ${node.name}`} wide onClose={onClose}>
      <div className="field">
        <label>新增规则</label>
        <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
          <select className="select" value={metric} onChange={(e) => setMetric(e.target.value)} style={{ width: 'auto' }}>
            {ALERT_METRICS.map((m) => (
              <option key={m.key} value={m.key}>{m.label}{m.unit ? ` (${m.unit})` : ''}</option>
            ))}
          </select>
          <input className="input" type="number" style={{ width: 110 }} placeholder="阈值" value={threshold} onChange={(e) => setThreshold(e.target.value)} />
          <input className="input" type="number" style={{ width: 130 }} placeholder="持续秒" value={windowSec} onChange={(e) => setWindowSec(e.target.value)} />
          <button className="btn primary" onClick={add} disabled={busy}>
            <Plus size={14} /> 添加
          </button>
        </div>
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 6 }}>
          指标超过阈值并持续指定秒数后推送一次 Telegram；恢复后重新计次。平均负载选「核心数×2」项时阈值填 0 即按节点 CPU 核数自动算；其余为百分比。
        </div>
      </div>

      <div className="field">
        <label>已有规则</label>
        {loading ? (
          <div style={{ color: 'var(--text-tertiary)', fontSize: 13 }}>加载中…</div>
        ) : alerts.length === 0 ? (
          <div style={{ color: 'var(--text-tertiary)', fontSize: 13 }}>暂无规则</div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {alerts.map((a) =>
              editing && editing.id === a.id ? (
                <div key={a.id} className="row" style={{ flexWrap: 'wrap', gap: 8, alignItems: 'center', padding: '8px 12px', border: '1px solid var(--primary)', borderRadius: 8 }}>
                  <select className="select" style={{ width: 'auto' }} value={editing.metric} onChange={(e) => setEditing({ ...editing, metric: e.target.value })}>
                    {ALERT_METRICS.map((m) => (
                      <option key={m.key} value={m.key}>{m.label}</option>
                    ))}
                  </select>
                  <input className="input" type="number" style={{ width: 100 }} value={editing.threshold} onChange={(e) => setEditing({ ...editing, threshold: parseFloat(e.target.value) || 0 })} />
                  <input className="input" type="number" style={{ width: 90 }} value={editing.window_sec} onChange={(e) => setEditing({ ...editing, window_sec: parseInt(e.target.value, 10) || 0 })} />
                  <label className="row" style={{ gap: 4, fontSize: 12 }}>
                    <input type="checkbox" checked={editing.enabled} onChange={(e) => setEditing({ ...editing, enabled: e.target.checked })} />
                    启用
                  </label>
                  <button className="btn primary sm" onClick={saveEdit} disabled={busy}>保存</button>
                  <button className="btn sm" onClick={() => setEditing(null)}>取消</button>
                </div>
              ) : (
                <div key={a.id} className="row" style={{ justifyContent: 'space-between', padding: '8px 12px', border: '1px solid var(--border-color)', borderRadius: 8 }}>
                  <span style={{ fontSize: 13 }}>
                    {alertMetricLabel(a.metric)} &gt; <strong>{a.threshold === 0 && a.metric === 'load' ? '核心数×2' : a.threshold}{alertMetricUnit(a.metric)}</strong>
                    <span style={{ color: 'var(--text-tertiary)' }}> · 持续 {a.window_sec}s</span>
                    {!a.enabled && <span className="badge muted" style={{ marginLeft: 6 }}>已停用</span>}
                  </span>
                  <div className="row" style={{ gap: 2 }}>
                    <button className="icon-btn" title="编辑" onClick={() => setEditing({ ...a })}>
                      <Pencil size={14} />
                    </button>
                    <button className="icon-btn" title="删除" onClick={() => remove(a.id)}>
                      <Trash2 size={14} />
                    </button>
                  </div>
                </div>
              )
            )}
          </div>
        )}
      </div>

      <div className="row" style={{ justifyContent: 'flex-end', marginTop: 12 }}>
        <button className="btn" onClick={onClose}>关闭</button>
      </div>
    </Modal>
  );
}

// TemplateModal: toggle which metrics are collected/rendered, and edit the
// default alert rules seeded into nodes. Reset restores factory defaults and
// re-seeds every installed node's alerts.
function TemplateModal({ onClose, current, changed }: { onClose: () => void; current: Tmpl | null; changed: (t: Tmpl) => void }) {
  const [catalog, setCatalog] = useState<any[]>([]);
  const [enabled, setEnabled] = useState<Set<string>>(new Set(current?.enabled || []));
  const [alerts, setAlerts] = useState<any[]>(current?.alerts || []);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.health.getTemplate().then((r: any) => {
      setCatalog(Array.isArray(r?.catalog) ? r.catalog : []);
      // Refresh working set if current was stale.
      if (!current) {
        setEnabled(new Set(r?.template?.enabled || []));
        setAlerts(r?.template?.alerts || []);
      }
    }).catch(() => { /* ignore */ });
  }, []);

  const toggle = (k: string) =>
    setEnabled((prev) => {
      const n = new Set(prev);
      n.has(k) ? n.delete(k) : n.add(k);
      return n;
    });

  const save = async () => {
    if (enabled.size === 0) return notify('至少启用一个指标', 'error');
    setBusy(true);
    try {
      const payload = { enabled: Array.from(enabled), alerts };
      const r: any = await api.health.putTemplate(payload);
      notify('模板已保存', 'success');
      changed(r?.template || payload);
      onClose();
    } catch (e: any) {
      notify(e?.response?.data?.error || '保存失败', 'error');
    } finally { setBusy(false); }
  };

  const reset = async () => {
    if (!confirm('恢复默认模板？这会把所有已装节点的告警规则重置为默认值（手动加的规则会丢失）。')) return;
    setBusy(true);
    try {
      const r: any = await api.health.resetTemplate();
      const t = r?.template;
      setEnabled(new Set(t?.enabled || []));
      setAlerts(t?.alerts || []);
      notify('已恢复默认', 'success');
      changed(t);
    } catch (e: any) {
      notify(e?.response?.data?.error || '恢复失败', 'error');
    } finally { setBusy(false); }
  };

  const updateAlert = (i: number, patch: any) =>
    setAlerts((prev) => prev.map((a, idx) => (idx === i ? { ...a, ...patch } : a)));
  const addAlert = () =>
    setAlerts((prev) => [...prev, { metric: 'cpu', threshold: 90, window_sec: 300 }]);
  const removeAlert = (i: number) => setAlerts((prev) => prev.filter((_, idx) => idx !== i));

  return (
    <Modal title="监控模板" wide onClose={onClose}>
      <div className="field">
        <label>监控指标（勾选 = 采集并展示）</label>
        <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
          {catalog.map((m) => (
            <label key={m.key} className="row" style={{ gap: 5, padding: '5px 10px', border: '1px solid var(--border-color)', borderRadius: 8, cursor: 'pointer', fontSize: 13, opacity: enabled.has(m.key) ? 1 : 0.6 }}>
              <input type="checkbox" checked={enabled.has(m.key)} onChange={() => toggle(m.key)} />
              {m.label}
            </label>
          ))}
        </div>
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 6 }}>
          磁盘空间汇总各挂载点显示一个总用量百分比。关掉某指标后对应卡片消失、也不再采集。
        </div>
      </div>

      <div className="field">
        <label>默认告警规则（新装节点 / 「恢复默认」时套用）</label>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {alerts.map((a, i) => (
            <div key={i} className="row" style={{ gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
              <select className="select" value={a.metric} onChange={(e) => updateAlert(i, { metric: e.target.value })} style={{ width: 'auto' }}>
                {ALERT_METRICS.map((m) => (
                  <option key={m.key} value={m.key}>{m.label}</option>
                ))}
              </select>
              <input className="input" type="number" style={{ width: 90 }} value={a.threshold} onChange={(e) => updateAlert(i, { threshold: parseFloat(e.target.value) || 0 })} />
              <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>&gt; 持续</span>
              <input className="input" type="number" style={{ width: 80 }} value={a.window_sec} onChange={(e) => updateAlert(i, { window_sec: parseInt(e.target.value) || 0 })} />
              <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>s</span>
              <button className="icon-btn" title="删除" onClick={() => removeAlert(i)}><Trash2 size={14} /></button>
            </div>
          ))}
        </div>
        <button className="btn sm ghost" style={{ marginTop: 8 }} onClick={addAlert}><Plus size={13} /> 添加规则</button>
      </div>

      <div className="modal-actions" style={{ justifyContent: 'space-between', marginTop: 12 }}>
        <button className="btn ghost" onClick={reset} disabled={busy}><RotateCcw size={14} /> 恢复默认</button>
        <div className="modal-actions" style={{ gap: 8, marginTop: 0 }}>
          <button className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" onClick={save} disabled={busy}>保存</button>
        </div>
      </div>
    </Modal>
  );
}

// ManageHealthModal: batch install/uninstall Netdata across nodes. Multi-select
// with 全选/反选/清空 + search (mirrors NodePickerModal), but with TWO actions on
// the selection (install / uninstall) instead of a single confirm. Defaults to
// selecting online nodes that aren't installed yet, so a one-click install is the
// common path. Used by low-spec nodes to turn monitoring off (uninstall frees
// Netdata's ~100MB) and back on.
function ManageHealthModal({
  nodes,
  onClose,
  onInstall,
  onUninstall,
}: {
  nodes: any[];
  installedCount: number;
  onClose: () => void;
  onInstall: (ids: string[]) => Promise<void>;
  onUninstall: (ids: string[]) => Promise<void>;
}) {
  // Default: online nodes that aren't installed yet.
  const [pick, setPick] = useState<string[]>(() =>
    nodes.filter((n: any) => (n.online || n.status === 'online') && !n.installed).map((n: any) => n.node_id)
  );
  const [q, setQ] = useState('');
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);

  const isOnline = (n: any) => n.online || n.status === 'online';
  const pool = nodes.filter((n: any) => (n.name || '').toLowerCase().includes(q.toLowerCase()));
  const has = (id: string) => pick.includes(id);
  const toggle = (id: string) => setPick((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));
  const selectAll = () => setPick(pool.map((n: any) => n.node_id));
  const invert = () => setPick(pool.filter((n: any) => !has(n.node_id)).map((n: any) => n.node_id));
  const clearAll = () => setPick([]);

  const doInstall = async () => {
    if (pick.length === 0) return;
    setBusy(true);
    try { await onInstall(pick); } finally { setBusy(false); onClose(); }
  };
  const doUninstall = async () => {
    setConfirming(false);
    if (pick.length === 0) return;
    setBusy(true);
    try { await onUninstall(pick); } finally { setBusy(false); onClose(); }
  };

  return (
    <Modal title="安装 / 卸载节点监控" wide onClose={onClose}>
      <div className="row" style={{ gap: 6, marginBottom: 10, flexWrap: 'wrap' }}>
        <button className="btn sm ghost" onClick={selectAll} disabled={busy}>
          <CheckCheck size={13} /> 全选
        </button>
        <button className="btn sm ghost" onClick={invert} disabled={busy}>
          <Square size={13} /> 反选
        </button>
        <button className="btn sm ghost" onClick={clearAll} disabled={busy}>
          清空
        </button>
        <span style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--text-tertiary)', alignSelf: 'center' }}>
          已选 {pick.length} · 共 {nodes.length}
        </span>
      </div>
      <div className="row" style={{ gap: 6, marginBottom: 10, alignItems: 'center' }}>
        <Search size={14} color="var(--text-tertiary)" />
        <input
          className="input"
          style={{ flex: 1 }}
          placeholder="搜索节点名…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          disabled={busy}
        />
      </div>
      <div style={{ maxHeight: 300, overflowY: 'auto', border: '1px solid var(--border-color)', borderRadius: 8 }}>
        {pool.length === 0 ? (
          <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-tertiary)' }}>无匹配节点</div>
        ) : (
          pool.map((n: any) => (
            <label
              key={n.node_id}
              className="row"
              style={{ padding: '8px 12px', cursor: busy ? 'default' : 'pointer', opacity: busy ? 0.6 : 1, borderBottom: '1px solid var(--border-color)' }}
              onClick={() => !busy && toggle(n.node_id)}
            >
              <input type="checkbox" checked={has(n.node_id)} readOnly />
              <span style={{ fontSize: 13, display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                {n.name}{' '}
                <span style={{ color: 'var(--text-tertiary)' }}>· {isOnline(n) ? '在线' : '离线'}</span>
                {n.installed ? (
                  <span className="badge success" style={{ fontSize: 11 }}>✓ 已安装</span>
                ) : (
                  <span className="badge muted" style={{ fontSize: 11 }}>未安装</span>
                )}
              </span>
            </label>
          ))
        )}
      </div>
      <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 8 }}>
        安装会下载并启动 Netdata（仅本地 127.0.0.1，不接入 Netdata Cloud）；卸载会彻底删除它以释放节点资源。离线节点无法操作。
      </div>
      <div className="modal-actions" style={{ justifyContent: 'flex-end', marginTop: 12 }}>
        <button className="btn" onClick={onClose} disabled={busy}>关闭</button>
        <button className="btn" onClick={() => setConfirming(true)} disabled={busy || pick.length === 0} title="从选中的节点删除 Netdata">
          <Trash2 size={14} /> 从选中卸载（{pick.length}）
        </button>
        <button className="btn primary" onClick={doInstall} disabled={busy || pick.length === 0} title="给选中的节点安装 Netdata">
          <Download size={14} /> 安装到选中（{pick.length}）
        </button>
      </div>
      {busy && <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 8 }}>处理中…安装需等 Netdata 首启（低配节点可能 30-75 秒），请勿关闭。</div>}
      {confirming && (
        <ConfirmModal
          title="批量卸载 Netdata"
          message={`确认从选中的 ${pick.length} 个节点卸载 Netdata？\n\n将停止这些节点的监控并删除 Netdata（释放内存/CPU）。告警规则保留，重装后自动恢复。`}
          confirmText={`卸载 ${pick.length} 个`}
          danger
          busy={busy}
          onClose={() => setConfirming(false)}
          onConfirm={doUninstall}
        />
      )}
    </Modal>
  );
}

function fmtBytes(b: number | undefined): string {
  if (b == null) return '—';
  if (b < 1024) return fmt(b, 0) + ' KB';
  if (b < 1024 * 1024) return (b / 1024).toFixed(1) + ' MB';
  return (b / 1024 / 1024).toFixed(1) + ' GB';
}
