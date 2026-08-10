import { useEffect, useState } from 'react';
import {
  Waypoints, Globe, RefreshCw, Plus, Play, Square, Trash2, Pencil, ArrowRightLeft,
  Save, ExternalLink,
} from 'lucide-react';
import { api } from '../services/api';
import { notify } from '../stores';
import { CopyButton, Empty, Loading, Modal } from '../components/ui';

// --- domain rule types (merged in from the former 域名 panel) ---
type DNSInfo = { target: string; proxied: boolean; matches: boolean } | null;
type Rule = {
  hostname: string;
  path?: string;
  service: string;
  is_catch_all: boolean;
  dns?: DNSInfo;
};

// --- merged tunnel: lifecycle (from /api/tunnels) + ingress (from /api/domains) ---
type MergedTunnel = {
  id: string;
  name: string;
  status?: string; // CF-side: healthy/degraded/down/inactive
  process?: string; // node-side cloudflared state: active/inactive/… (systemd or docker)
  version?: string;
  managed: boolean; // panel-provisioned (systemd) vs hand-built (docker) — info only
  online: boolean; // node reachable for probing
  node?: { id: string; name: string } | null;
  cname_target: string; // expected DNS target (<id>.cfargotunnel.com)
  error?: string; // ingress config read failure
  rules: Rule[];
};

type DomainModal =
  | { kind: 'add'; tunnelId?: string }
  | { kind: 'edit'; tunnel: MergedTunnel; rule: Rule }
  | { kind: 'move'; tunnel: MergedTunnel; rule: Rule }
  | null;

export function TunnelsPage() {
  const [tunnels, setTunnels] = useState<MergedTunnel[]>([]);
  const [nodes, setNodes] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [noToken, setNoToken] = useState(false);
  const [busy, setBusy] = useState<Record<string, string>>({}); // id → in-flight lifecycle action
  const [createModal, setCreateModal] = useState(false);
  const [renaming, setRenaming] = useState<MergedTunnel | null>(null);
  const [domainModal, setDomainModal] = useState<DomainModal>(null);

  const load = async () => {
    setLoading(true);
    setNoToken(false);
    try {
      const [t, d, n]: any[] = await Promise.all([
        api.tunnels.list(),
        api.domains.list(),
        api.nodes.list().catch(() => []),
      ]);
      setTunnels(mergeTunnels(t?.tunnels || [], d?.tunnels || []));
      setNodes(Array.isArray(n) ? n : []);
    } catch (e: any) {
      const msg = e?.response?.data?.error || '';
      if (msg.includes('未配置 Cloudflare')) setNoToken(true);
      else notify(msg || '加载失败', 'error');
      setTunnels([]);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  // tunnel lifecycle action (start/stop/delete)
  const act = async (t: MergedTunnel, action: 'start' | 'stop' | 'del') => {
    const verb = action === 'start' ? '启动' : action === 'stop' ? '停止' : '删除';
    if (
      action === 'del' &&
      !confirm(
        `删除隧道「${t.name}」？\n将删除 CF 隧道、停止并清理节点上的 cloudflared、删除该隧道各 ingress 域名对应的 DNS CNAME 记录、解绑节点。不可恢复。`,
      )
    )
      return;
    setBusy((b) => ({ ...b, [t.id]: action }));
    try {
      const r: any =
        action === 'del'
          ? await api.tunnels.del(t.id)
          : action === 'start'
            ? await api.tunnels.start(t.id)
            : await api.tunnels.stop(t.id);
      notify(verb + '成功' + (r?.note ? '（' + r.note + '）' : ''), 'success');
      load();
    } catch (e: any) {
      notify(e?.response?.data?.error || verb + '失败', 'error');
    } finally {
      setBusy((b) => {
        const c = { ...b };
        delete c[t.id];
        return c;
      });
    }
  };

  // domain rule delete (ingress)
  const onDeleteRule = async (tunnel: MergedTunnel, rule: Rule) => {
    if (rule.is_catch_all) return notify('不能删除兜底规则', 'error');
    if (!confirm(`删除域名 ${rule.hostname} 的指向规则？\n删除后该域名将落到 catch-all 返回 404（DNS 记录不自动清理）。`))
      return;
    try {
      await api.domains.deleteRule(tunnel.id, rule.hostname, rule.path || '');
      notify(`已删除 ${rule.hostname}，cloudflared 将在数秒内自动生效`, 'success');
      load();
    } catch (e: any) {
      notify(e?.response?.data?.error || '删除失败', 'error');
    }
  };

  return (
    <div>
      <div className="spread" style={{ marginBottom: 14 }}>
        <div>
          <h1 className="page-title">隧道</h1>
          <p className="page-subtitle" style={{ marginBottom: 0 }}>
            Cloudflare Tunnel：隧道生命周期（启停 / 重命名 / 删除）+ 各隧道的域名指向（ingress 规则，编辑后数秒热更新）。任意隧道（含手建）均可操作。
          </p>
        </div>
        <div className="page-actions">
          <button className="btn ghost" onClick={load} disabled={loading}>
            <RefreshCw size={15} /> 刷新
          </button>
          <button className="btn primary" onClick={() => setDomainModal({ kind: 'add' })} disabled={noToken}>
            <Globe size={15} /> 新增域名
          </button>
          <button className="btn primary" onClick={() => setCreateModal(true)} disabled={noToken}>
            <Plus size={15} /> 新建隧道
          </button>
        </div>
      </div>

      {noToken && (
        <div className="card" style={{ padding: 18, maxWidth: 620 }}>
          <div className="row" style={{ gap: 10, marginBottom: 6 }}>
            <Waypoints size={18} color="var(--primary)" />
            <strong>未配置 Cloudflare 令牌</strong>
          </div>
          <p style={{ marginTop: 0, color: 'var(--text-secondary)' }}>
            隧道板块需要 Cloudflare API token（账号级 <code>Tunnel:Edit</code> + <code>DNS:Edit</code> 权限）来管理隧道与域名指向。
          </p>
          <a className="btn primary sm" href="/settings" style={{ textDecoration: 'none' }}>
            前往「设置 → Cloudflare」配置 <ExternalLink size={13} />
          </a>
        </div>
      )}

      {loading && <Loading />}

      {!loading && !noToken && tunnels.length === 0 && (
        <Empty text="没有 tunnel，点「新建隧道」创建" />
      )}

      {!loading &&
        tunnels.map((t) => (
          <TunnelCard
            key={t.id}
            tunnel={t}
            busy={busy}
            act={act}
            onRename={() => setRenaming(t)}
            onAddRule={() => setDomainModal({ kind: 'add', tunnelId: t.id })}
            onEditRule={(rule) => setDomainModal({ kind: 'edit', tunnel: t, rule })}
            onMoveRule={(rule) => setDomainModal({ kind: 'move', tunnel: t, rule })}
            onDeleteRule={(rule) => onDeleteRule(t, rule)}
          />
        ))}

      {createModal && (
        <CreateModal
          nodes={nodes}
          onClose={() => setCreateModal(false)}
          onDone={() => {
            setCreateModal(false);
            notify('隧道已创建并启动，CF 状态将在约 10 秒内变为 healthy', 'success');
            load();
          }}
        />
      )}

      {renaming && (
        <RenameModal
          tunnel={renaming}
          onClose={() => setRenaming(null)}
          onDone={() => {
            setRenaming(null);
            notify('隧道已重命名（连接不受影响）', 'success');
            load();
          }}
        />
      )}

      {domainModal?.kind === 'add' && (
        <AddModal
          tunnels={tunnels}
          initialTunnelId={domainModal.tunnelId}
          onClose={() => setDomainModal(null)}
          onDone={() => {
            setDomainModal(null);
            notify('已新增规则，cloudflared 将在数秒内自动生效', 'success');
            load();
          }}
        />
      )}
      {domainModal?.kind === 'edit' && (
        <EditModal
          tunnel={domainModal.tunnel}
          rule={domainModal.rule}
          onClose={() => setDomainModal(null)}
          onDone={() => {
            setDomainModal(null);
            notify('已更新指向，cloudflared 将在数秒内自动生效', 'success');
            load();
          }}
        />
      )}
      {domainModal?.kind === 'move' && (
        <MoveModal
          tunnel={domainModal.tunnel}
          rule={domainModal.rule}
          tunnels={tunnels}
          onClose={() => setDomainModal(null)}
          onDone={(note?: string) => {
            setDomainModal(null);
            notify(note || '域名已移动，cloudflared 将在数秒内自动生效', 'success');
            load();
          }}
        />
      )}
    </div>
  );
}

// mergeTunnels joins the lifecycle view (/api/tunnels) with the ingress view
// (/api/domains) by tunnel id. domains carries rules + cname_target + dns;
// tunnels overlays node-side process/version + managed/online.
function mergeTunnels(tList: any[], dList: any[]): MergedTunnel[] {
  const byId = new Map<string, MergedTunnel>();
  for (const d of dList) {
    byId.set(d.id, {
      id: d.id,
      name: d.name,
      status: d.status,
      node: d.node,
      cname_target: d.cname_target || '',
      error: d.error,
      rules: Array.isArray(d.rules) ? d.rules : [],
      managed: false,
      online: false,
    });
  }
  for (const t of tList) {
    const ex = byId.get(t.id);
    if (ex) {
      ex.process = t.process;
      ex.version = t.version;
      ex.managed = !!t.managed;
      ex.online = !!t.online;
      if (!ex.name) ex.name = t.name;
      if (!ex.status) ex.status = t.status;
      if (!ex.node && t.node) ex.node = t.node;
    } else {
      byId.set(t.id, {
        id: t.id,
        name: t.name,
        status: t.status,
        node: t.node,
        process: t.process,
        version: t.version,
        managed: !!t.managed,
        online: !!t.online,
        cname_target: '',
        rules: [],
        error: '未读取到 ingress 配置',
      });
    }
  }
  return [...byId.values()];
}

function cfBadge(status?: string) {
  if (!status) return null;
  const ok = status === 'healthy';
  const warn = status === 'degraded';
  return (
    <span className={`badge ${ok ? 'success' : warn ? 'warning' : 'muted'}`}>
      <span className={`status-dot ${ok ? 'online' : 'offline'}`} /> CF {status}
    </span>
  );
}

function procBadge(proc?: string) {
  if (!proc) return <span className="badge muted">进程未知</span>;
  const active = proc === 'active';
  return <span className={`badge ${active ? 'success' : 'warning'}`}>{active ? '进程运行中' : proc}</span>;
}

function TunnelCard({
  tunnel: t,
  busy,
  act,
  onRename,
  onAddRule,
  onEditRule,
  onMoveRule,
  onDeleteRule,
}: {
  tunnel: MergedTunnel;
  busy: Record<string, string>;
  act: (t: MergedTunnel, action: 'start' | 'stop' | 'del') => void;
  onRename: () => void;
  onAddRule: () => void;
  onEditRule: (r: Rule) => void;
  onMoveRule: (r: Rule) => void;
  onDeleteRule: (r: Rule) => void;
}) {
  const locked = busy[t.id];
  return (
    <div className="card" style={{ padding: 16, marginBottom: 14 }}>
      <div className="spread" style={{ marginBottom: 8, flexWrap: 'wrap', gap: 8 }}>
        <div className="row" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
          <Waypoints size={17} color="var(--primary)" />
          <strong style={{ fontSize: 15 }}>{t.name || '(未命名)'}</strong>
          {cfBadge(t.status)}
          {procBadge(t.process)}
          {t.managed ? (
            <span className="badge success">面板托管</span>
          ) : (
            <span className="badge muted">手建</span>
          )}
          {t.node ? (
            <span className="badge muted">
              节点：{t.node.name}
              {!t.online ? '（离线）' : ''}
            </span>
          ) : (
            <span className="badge muted">未关联节点</span>
          )}
        </div>
        <div className="row" style={{ gap: 4 }}>
          <button className="icon-btn" title="重命名" disabled={!!locked} onClick={onRename}>
            <Pencil size={15} />
          </button>
          <button className="icon-btn" title="启动" disabled={!!locked} onClick={() => act(t, 'start')}>
            <Play size={15} />
          </button>
          <button className="icon-btn" title="停止" disabled={!!locked} onClick={() => act(t, 'stop')}>
            <Square size={15} />
          </button>
          <button className="icon-btn" title="删除" disabled={!!locked} onClick={() => act(t, 'del')}>
            <Trash2 size={15} />
          </button>
        </div>
      </div>

      <div className="row" style={{ fontSize: 12, color: 'var(--text-tertiary)', flexWrap: 'wrap', gap: 10, marginBottom: 10 }}>
        <span>
          {locked && <span>{locked === 'del' ? '删除中…' : locked === 'start' ? '启动中…' : '停止中…'} · </span>}
          {t.version && <span>cloudflared {t.version}</span>}
          {!t.node && <span> · 未关联节点：可重命名 / 删除，启停请到 CF 控制台</span>}
        </span>
        {t.cname_target && (
          <span className="row" style={{ alignItems: 'center', gap: 4 }}>
            应指向
            <code className="mono" style={{ fontSize: 11 }}>
              {t.cname_target}
            </code>
            <CopyButton text={t.cname_target} title="复制期望 CNAME" />
          </span>
        )}
      </div>

      {t.error ? (
        <div className="badge error" style={{ display: 'block', padding: 8 }}>
          读取域名配置失败：{t.error}
        </div>
      ) : (
        <div>
          <div
            className="row rule-row-head"
            style={{
              fontSize: 12,
              color: 'var(--text-tertiary)',
              padding: '0 6px 6px',
              borderBottom: '1px solid var(--border-color)',
            }}
          >
            <div style={{ flex: '0 0 30%' }}>域名 / 路径</div>
            <div style={{ flex: '0 0 26%' }}>指向 (service)</div>
            <div style={{ flex: 1 }}>DNS CNAME 状态</div>
            <div style={{ flex: '0 0 120px', textAlign: 'right' }}>
              <button className="icon-btn" title="新增域名到此隧道" onClick={onAddRule}>
                <Plus size={15} />
              </button>
            </div>
          </div>
          <div className="row" style={{ justifyContent: 'flex-end', marginBottom: 6 }} data-mobile-only>
            <button className="btn sm" onClick={onAddRule}>
              <Plus size={14} /> 新增域名
            </button>
          </div>
          {t.rules.map((rule, i) => (
            <RuleRow
              key={i}
              rule={rule}
              onEdit={() => onEditRule(rule)}
              onMove={() => onMoveRule(rule)}
              onDelete={() => onDeleteRule(rule)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function RuleRow({
  rule,
  onEdit,
  onMove,
  onDelete,
}: {
  rule: Rule;
  onEdit: () => void;
  onMove: () => void;
  onDelete: () => void;
}) {
  return (
    <div
      className="row rule-row"
      style={{
        padding: '8px 6px',
        borderBottom: '1px solid var(--border-color)',
        alignItems: 'center',
        flexWrap: 'wrap',
        gap: 6,
      }}
    >
      <div style={{ flex: '0 0 30%', minWidth: 180 }}>
        <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginBottom: 2 }} className="rule-field-label">域名 / 路径</div>
        {rule.is_catch_all ? (
          <span className="badge muted">兜底 (catch-all)</span>
        ) : (
          <>
            <div className="mono break-anywhere" style={{ fontSize: 13 }}>
              {rule.hostname}
            </div>
            {rule.path && (
              <div className="mono" style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
                path: {rule.path}
              </div>
            )}
          </>
        )}
      </div>
      <div style={{ flex: '0 0 26%', minWidth: 160 }}>
        <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginBottom: 2 }} className="rule-field-label">指向</div>
        <code className="mono break-anywhere" style={{ fontSize: 12 }}>
          {rule.service}
        </code>
      </div>
      <div style={{ flex: 1, minWidth: 200 }}>
        <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginBottom: 2 }} className="rule-field-label">DNS</div>
        <DNSBadge rule={rule} />
      </div>
      <div className="card-actions" style={{ flex: '0 0 120px', marginTop: 0, gridTemplateColumns: 'repeat(3, minmax(0, 1fr))' }}>
        <button className="icon-btn" title="编辑指向" onClick={onEdit}>
          <Pencil size={15} />
        </button>
        {!rule.is_catch_all && (
          <>
            <button className="icon-btn" title="移动到其它隧道" onClick={onMove}>
              <ArrowRightLeft size={15} />
            </button>
            <button className="icon-btn" title="删除规则" onClick={onDelete}>
              <Trash2 size={15} />
            </button>
          </>
        )}
      </div>
    </div>
  );
}

function DNSBadge({ rule }: { rule: Rule }) {
  if (rule.is_catch_all) return <span style={{ color: 'var(--text-tertiary)' }}>—</span>;
  const dns = rule.dns;
  if (!dns) return <span style={{ color: 'var(--text-tertiary)' }}>—</span>;
  if (!dns.target) return <span className="badge warning">无 CNAME 记录</span>;
  return (
    <div className="row" style={{ gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
      <span className={`badge ${dns.matches ? 'success' : 'warning'}`}>
        {dns.matches ? '✓ 匹配' : '✗ 不匹配'}
      </span>
      <code className="mono" style={{ fontSize: 11 }}>
        {dns.target}
      </code>
      {!dns.proxied && <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>(DNS-only)</span>}
    </div>
  );
}

function ServiceHint() {
  return (
    <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
      源站 service，如 <code>http://localhost:8088</code>、<code>https://127.0.0.1:8443</code>、<code>tcp://localhost:25565</code>。
    </div>
  );
}

function CreateModal({
  nodes,
  onClose,
  onDone,
}: {
  nodes: any[];
  onClose: () => void;
  onDone: () => void;
}) {
  const online = nodes.filter((n) => n.online);
  const [nodeId, setNodeId] = useState(online[0]?.id || '');
  const [name, setName] = useState('');
  const [saving, setSaving] = useState(false);

  const save = async () => {
    if (!nodeId) return notify('请选择节点', 'error');
    if (!/^[a-zA-Z0-9_-]+$/.test(name.trim()))
      return notify('隧道名只能含字母、数字、下划线、连字符', 'error');
    setSaving(true);
    try {
      await api.tunnels.create(nodeId, name.trim());
      onDone();
    } catch (e: any) {
      notify(e?.response?.data?.error || '创建失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title="新建隧道"
      onClose={onClose}
      footer={
        <>
          <button className="btn ghost sm" onClick={onClose}>
            取消
          </button>
          <button className="btn primary sm" onClick={save} disabled={saving}>
            <Plus size={14} /> {saving ? '创建中…' : '创建'}
          </button>
        </>
      }
    >
      <p style={{ marginTop: 0, color: 'var(--text-secondary)', fontSize: 13 }}>
        在所选节点上创建一条 Cloudflare Tunnel：面板会自动安装 cloudflared、注册并以 systemd 服务启动它。
      </p>
      <div className="field">
        <label>目标节点</label>
        <select className="select" value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
          {online.length === 0 && <option value="">没有在线节点</option>}
          {online.map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
              {n.ingress_type === 'external' ? '（NPM / external 入站）' : ''}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label>隧道名</label>
        <input className="input mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="如 utah、dama" />
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
          仅字母 / 数字 / 下划线 / 连字符；将作为节点上的 systemd 服务名（cloudflared- 加该名称）。
        </div>
      </div>
    </Modal>
  );
}

function RenameModal({
  tunnel,
  onClose,
  onDone,
}: {
  tunnel: MergedTunnel;
  onClose: () => void;
  onDone: () => void;
}) {
  const [name, setName] = useState(tunnel.name || '');
  const [saving, setSaving] = useState(false);

  const save = async () => {
    const trimmed = name.trim();
    if (!/^[a-zA-Z0-9_-]+$/.test(trimmed))
      return notify('隧道名只能含字母、数字、下划线、连字符', 'error');
    if (trimmed === tunnel.name) return onClose();
    setSaving(true);
    try {
      await api.tunnels.rename(tunnel.id, trimmed);
      onDone();
    } catch (e: any) {
      notify(e?.response?.data?.error || '重命名失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={`重命名隧道（当前：${tunnel.name || '(未命名)'}）`}
      onClose={onClose}
      footer={
        <>
          <button className="btn ghost sm" onClick={onClose}>
            取消
          </button>
          <button className="btn primary sm" onClick={save} disabled={saving}>
            {saving ? '保存中…' : '保存'}
          </button>
        </>
      }
    >
      <p style={{ marginTop: 0, color: 'var(--text-secondary)', fontSize: 13 }}>
        只改隧道名，tunnel id 与运行中的连接均不受影响。
      </p>
      <div className="field">
        <label>新隧道名</label>
        <input
          className="input mono"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={tunnel.name || '如 utah、dama'}
          autoFocus
        />
      </div>
    </Modal>
  );
}

function AddModal({
  tunnels,
  initialTunnelId,
  onClose,
  onDone,
}: {
  tunnels: MergedTunnel[];
  initialTunnelId?: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const [tunnelId, setTunnelId] = useState(initialTunnelId || tunnels[0]?.id || '');
  const [hostname, setHostname] = useState('');
  const [path, setPath] = useState('');
  const [service, setService] = useState('http://localhost:');
  const [saving, setSaving] = useState(false);

  const save = async () => {
    if (!tunnelId || !hostname.trim() || !service.trim()) return notify('隧道、域名、指向均不能为空', 'error');
    setSaving(true);
    try {
      await api.domains.addRule(tunnelId, hostname.trim(), service.trim(), path.trim());
      onDone();
    } catch (e: any) {
      notify(e?.response?.data?.error || '新增失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title="新增域名规则"
      onClose={onClose}
      footer={
        <>
          <button className="btn ghost sm" onClick={onClose}>
            取消
          </button>
          <button className="btn primary sm" onClick={save} disabled={saving}>
            <Save size={14} /> {saving ? '保存中…' : '保存'}
          </button>
        </>
      }
    >
      <div className="field">
        <label>隧道（节点）</label>
        <select className="select" value={tunnelId} onChange={(e) => setTunnelId(e.target.value)}>
          {tunnels.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
              {t.node ? `（${t.node.name}）` : ''}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label>域名 (hostname)</label>
        <input className="input" value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="app.example.com" />
      </div>
      <div className="field">
        <label>路径 (path，可选)</label>
        <input className="input" value={path} onChange={(e) => setPath(e.target.value)} placeholder="留空匹配整个域名" />
      </div>
      <div className="field">
        <label>指向 (service)</label>
        <input className="input mono" value={service} onChange={(e) => setService(e.target.value)} />
        <ServiceHint />
      </div>
    </Modal>
  );
}

function EditModal({
  tunnel,
  rule,
  onClose,
  onDone,
}: {
  tunnel: MergedTunnel;
  rule: Rule;
  onClose: () => void;
  onDone: () => void;
}) {
  const [hostname, setHostname] = useState(rule.hostname);
  const [path, setPath] = useState(rule.path || '');
  const [service, setService] = useState(rule.service);
  const [saving, setSaving] = useState(false);

  const save = async () => {
    if (!hostname.trim() || !service.trim()) return notify('域名与指向不能为空', 'error');
    setSaving(true);
    try {
      await api.domains.editRule(tunnel.id, rule.hostname, hostname.trim(), service.trim(), rule.path || '', path.trim());
      onDone();
    } catch (e: any) {
      notify(e?.response?.data?.error || '保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={`编辑指向 — ${tunnel.name}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn ghost sm" onClick={onClose}>
            取消
          </button>
          <button className="btn primary sm" onClick={save} disabled={saving}>
            <Save size={14} /> {saving ? '保存中…' : '保存'}
          </button>
        </>
      }
    >
      <div className="field">
        <label>域名 (hostname)</label>
        <input className="input mono" value={hostname} onChange={(e) => setHostname(e.target.value)} disabled={rule.is_catch_all} />
      </div>
      {!rule.is_catch_all && (
        <div className="field">
          <label>路径 (path，可选)</label>
          <input className="input" value={path} onChange={(e) => setPath(e.target.value)} placeholder="留空匹配整个域名" />
        </div>
      )}
      <div className="field">
        <label>指向 (service)</label>
        <input className="input mono" value={service} onChange={(e) => setService(e.target.value)} />
        <ServiceHint />
      </div>
    </Modal>
  );
}

function MoveModal({
  tunnel,
  rule,
  tunnels,
  onClose,
  onDone,
}: {
  tunnel: MergedTunnel;
  rule: Rule;
  tunnels: MergedTunnel[];
  onClose: () => void;
  onDone: (note?: string) => void;
}) {
  const others = tunnels.filter((t) => t.id !== tunnel.id);
  const [toTunnel, setToTunnel] = useState(others[0]?.id || '');
  const [service, setService] = useState(rule.service);
  const [saving, setSaving] = useState(false);

  const save = async () => {
    if (!toTunnel) return notify('没有可移动的目标隧道', 'error');
    if (!service.trim()) return notify('请填写目标节点上的源站 service', 'error');
    setSaving(true);
    try {
      const r: any = await api.domains.move(rule.hostname, tunnel.id, toTunnel, service.trim());
      onDone(r?.note);
    } catch (e: any) {
      notify(e?.response?.data?.error || '移动失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={`移动域名 — ${rule.hostname}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn ghost sm" onClick={onClose}>
            取消
          </button>
          <button className="btn primary sm" onClick={save} disabled={saving}>
            <ArrowRightLeft size={14} /> {saving ? '移动中…' : '移动'}
          </button>
        </>
      }
    >
      <p style={{ marginTop: 0, color: 'var(--text-secondary)', fontSize: 13 }}>
        把该域名从 <strong>{tunnel.name}</strong> 搬到目标隧道：自动在目标加 ingress 规则、改 DNS CNAME、再删源规则（顺序保证不断服）。
      </p>
      <div className="field">
        <label>当前域名</label>
        <input className="input mono" value={rule.hostname} disabled />
      </div>
      <div className="field">
        <label>目标隧道（节点）</label>
        <select className="select" value={toTunnel} onChange={(e) => setToTunnel(e.target.value)}>
          {others.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
              {t.node ? `（${t.node.name}）` : ''}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label>目标节点上的源站 (service)</label>
        <input className="input mono" value={service} onChange={(e) => setService(e.target.value)} />
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
          填该域名在目标节点上实际监听的源站（端口通常与源节点不同）。默认沿用当前指向，请按需修改。
        </div>
      </div>
    </Modal>
  );
}
