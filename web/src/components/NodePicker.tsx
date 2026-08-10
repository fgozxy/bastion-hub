import { useState } from 'react';
import { CheckCheck, Square, X, Search } from 'lucide-react';
import { Modal } from './ui';

// NodeSelect is a project-styled multi-select node picker: a clickable chip box
// that opens a Modal with checkboxes + 全选/反选/清空. Replaces native <select>.
// installedIds (optional): node ids already provisioned with whatever this
// picker is targeting (e.g. Netdata) — rendered as a green "已安装" badge so the
// user can tell already-done nodes from pending ones at a glance.
export function NodeSelect({
  nodes,
  value,
  onChange,
  onlineOnly = false,
  placeholder = '选择节点…',
  installedIds,
}: {
  nodes: any[];
  value: string[];
  onChange: (ids: string[]) => void;
  onlineOnly?: boolean;
  placeholder?: string;
  installedIds?: string[];
}) {
  const [open, setOpen] = useState(false);
  const selectedNames = nodes.filter((n) => value.includes(n.id)).map((n) => n.name);
  const installedCount = (installedIds || []).filter((id) => nodes.some((n) => n.id === id)).length;

  return (
    <>
      <div
        className="input"
        style={{
          minHeight: 38,
          padding: '5px 10px',
          cursor: 'pointer',
          display: 'flex',
          flexWrap: 'wrap',
          gap: 6,
          alignItems: 'center',
        }}
        onClick={() => setOpen(true)}
      >
        {value.length === 0 ? (
          <span style={{ color: 'var(--text-tertiary)' }}>{placeholder}</span>
        ) : (
          selectedNames.map((nm, i) => (
            <span key={i} className="badge muted">
              {nm}
            </span>
          ))
        )}
        <span style={{ marginLeft: 'auto', color: 'var(--text-tertiary)', fontSize: 12 }}>
          {value.length} 个
          {installedCount > 0 && ` · 已装 ${installedCount}`}
        </span>
      </div>

      {open && (
        <NodePickerModal
          nodes={nodes}
          selected={value}
          onlineOnly={onlineOnly}
          installedIds={installedIds}
          onConfirm={(ids) => {
            onChange(ids);
            setOpen(false);
          }}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  );
}

function NodePickerModal({
  nodes,
  selected,
  onlineOnly,
  installedIds,
  onConfirm,
  onClose,
}: {
  nodes: any[];
  selected: string[];
  onlineOnly?: boolean;
  installedIds?: string[];
  onConfirm: (ids: string[]) => void;
  onClose: () => void;
}) {
  const [pick, setPick] = useState<string[]>(selected);
  const [q, setQ] = useState('');

  const isOnline = (n: any) => n.online || n.status === 'online';
  const installedSet = new Set(installedIds || []);
  const isInstalled = (n: any) => installedSet.has(n.id);
  const pool = nodes.filter((n) => (onlineOnly ? isOnline(n) : true)).filter((n) => (n.name || '').toLowerCase().includes(q.toLowerCase()));

  // Restore capability derived from agent version: >=1.8.0 supports preflight +
  // streaming progress; >=1.7.0 supports container rebuild (no preflight/progress);
  // older can only refill data. Old agents are warned, not blocked.
  const cap = (v?: string) => {
    const p = (v || '').split('.').map(Number);
    const maj = p[0] || 0;
    const min = p[1] || 0;
    const ge = (a: number, b: number) => maj > a || (maj === a && min >= b);
    if (ge(1, 8)) return { label: '支持预检/进度', cls: 'success' };
    if (ge(1, 7)) return { label: '支持重建·无预检', cls: 'warning' };
    return { label: '仅回填数据', cls: 'error' };
  };
  const has = (id: string) => pick.includes(id);
  const toggle = (id: string) => setPick((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));
  const allIds = () => pool.map((n) => n.id);
  const selectAll = () => setPick(allIds());
  const invert = () => setPick(pool.filter((n) => !has(n.id)).map((n) => n.id));
  const clearAll = () => setPick([]);

  return (
    <Modal
      title="选择节点"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" onClick={() => onConfirm(pick)} disabled={pick.length === 0}>
            确定（{pick.length}）
          </button>
        </>
      }
    >
      <div className="row" style={{ gap: 6, marginBottom: 10 }}>
        <button className="btn sm ghost" onClick={selectAll}>
          <CheckCheck size={13} /> 全选
        </button>
        <button className="btn sm ghost" onClick={invert}>
          <Square size={13} /> 反选
        </button>
        <button className="btn sm ghost" onClick={clearAll}>
          <X size={13} /> 清空
        </button>
        <span style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--text-tertiary)', alignSelf: 'center' }}>
          已选 {pick.length}
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
        />
      </div>
      <div style={{ maxHeight: 280, overflowY: 'auto', border: '1px solid var(--border-color)', borderRadius: 8 }}>
        {pool.length === 0 ? (
          <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-tertiary)' }}>无可用节点</div>
        ) : (
          pool.map((n) => (
            <label
              key={n.id}
              className="row"
              style={{ padding: '8px 12px', cursor: 'pointer', borderBottom: '1px solid var(--border-color)' }}
              onClick={() => toggle(n.id)}
            >
              <input type="checkbox" checked={has(n.id)} readOnly />
              <span style={{ fontSize: 13, display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                {n.name}{' '}
                <span style={{ color: 'var(--text-tertiary)' }}>· {isOnline(n) ? '在线' : '离线'}</span>
                {isInstalled(n) && (
                  <span className="badge success" style={{ fontSize: 11 }} title="该节点已安装 Netdata">
                    ✓ 已安装
                  </span>
                )}
                {n.ingress_type === 'external' && (
                  <span className="badge muted" style={{ fontSize: 11 }}>外部线路</span>
                )}
                {n.agent_version && (
                  <>
                    <span style={{ color: 'var(--text-tertiary)', fontSize: 11 }}>v{n.agent_version}</span>
                    <span className={`badge ${cap(n.agent_version).cls}`} style={{ fontSize: 11 }}>
                      {cap(n.agent_version).label}
                    </span>
                  </>
                )}
              </span>
            </label>
          ))
        )}
      </div>
    </Modal>
  );
}
