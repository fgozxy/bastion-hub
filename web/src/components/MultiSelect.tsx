import { useState } from 'react';
import { CheckCheck, Square, X, Search } from 'lucide-react';
import { Modal } from './ui';

export type SelectItem = { id: string; label: string; sub?: string };

// MultiSelect is a project-styled multi-select: a chip box that opens a Modal
// with checkboxes, 全选/反选/清空, and search. Replaces native multi-<select>.
export function MultiSelect({
  items,
  value,
  onChange,
  placeholder = '选择…',
  title = '选择',
  emptyText = '无可选项',
}: {
  items: SelectItem[];
  value: string[];
  onChange: (ids: string[]) => void;
  placeholder?: string;
  title?: string;
  emptyText?: string;
}) {
  const [open, setOpen] = useState(false);
  const selectedLabels = items.filter((it) => value.includes(it.id)).map((it) => it.label);

  return (
    <>
      <div
        className="input"
        style={{ minHeight: 38, padding: '5px 10px', cursor: 'pointer', display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}
        onClick={() => setOpen(true)}
      >
        {value.length === 0 ? (
          <span style={{ color: 'var(--text-tertiary)' }}>{placeholder}</span>
        ) : (
          selectedLabels.map((l, i) => <span key={i} className="badge muted">{l}</span>)
        )}
        <span style={{ marginLeft: 'auto', color: 'var(--text-tertiary)', fontSize: 12 }}>{value.length} 个</span>
      </div>

      {open && (
        <PickerModal
          title={title}
          items={items}
          selected={value}
          emptyText={emptyText}
          onConfirm={(ids) => { onChange(ids); setOpen(false); }}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  );
}

function PickerModal({
  title, items, selected, emptyText, onConfirm, onClose,
}: {
  title: string;
  items: SelectItem[];
  selected: string[];
  emptyText: string;
  onConfirm: (ids: string[]) => void;
  onClose: () => void;
}) {
  const [pick, setPick] = useState<string[]>(selected);
  const [q, setQ] = useState('');
  const pool = items.filter((it) => (it.label + ' ' + (it.sub || '')).toLowerCase().includes(q.toLowerCase()));
  const has = (id: string) => pick.includes(id);
  const toggle = (id: string) => setPick((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" onClick={() => onConfirm(pick)}>确定（{pick.length}）</button>
        </>
      }
    >
      <div className="row" style={{ gap: 6, marginBottom: 10 }}>
        <button className="btn sm ghost" onClick={() => setPick(pool.map((i) => i.id))}><CheckCheck size={13} /> 全选</button>
        <button className="btn sm ghost" onClick={() => setPick(pool.filter((i) => !has(i.id)).map((i) => i.id))}><Square size={13} /> 反选</button>
        <button className="btn sm ghost" onClick={() => setPick([])}><X size={13} /> 清空</button>
        <span style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--text-tertiary)', alignSelf: 'center' }}>已选 {pick.length}</span>
      </div>
      <div className="row" style={{ gap: 6, marginBottom: 10, alignItems: 'center' }}>
        <Search size={14} color="var(--text-tertiary)" />
        <input className="input" style={{ flex: 1 }} placeholder="搜索…" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      <div style={{ maxHeight: 280, overflowY: 'auto', border: '1px solid var(--border-color)', borderRadius: 8 }}>
        {pool.length === 0 ? (
          <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-tertiary)' }}>{emptyText}</div>
        ) : (
          pool.map((it) => (
            <label key={it.id} className="row" style={{ padding: '8px 12px', cursor: 'pointer', borderBottom: '1px solid var(--border-color)' }} onClick={() => toggle(it.id)}>
              <input type="checkbox" checked={has(it.id)} readOnly />
              <span style={{ fontSize: 13 }}>
                {it.label} {it.sub && <span style={{ color: 'var(--text-tertiary)' }}>· {it.sub}</span>}
              </span>
            </label>
          ))
        )}
      </div>
    </Modal>
  );
}
