import { useEffect, useRef, useState } from 'react';
import { Play, History, Shield, Save, Trash2, CheckCheck, Square, X } from 'lucide-react';
import { api } from '../services/api';
import { useWs } from '../hooks/useWs';
import { notify } from '../stores';
import { Empty } from '../components/ui';

type SavedCmd = { id: string; name: string; script: string; builtin: boolean };

export function CommandsPage() {
  const [nodes, setNodes] = useState<any[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [cmd, setCmd] = useState('');
  const [running, setRunning] = useState(false);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [output, setOutput] = useState<Record<string, { stream: string; data: string }[]>>({});
  const [history, setHistory] = useState<any[]>([]);
  const [saved, setSaved] = useState<SavedCmd[]>([]);
  const outRef = useRef<HTMLDivElement>(null);

  const loadAll = () => {
    api.nodes.list().then((r: any) => setNodes(Array.isArray(r) ? r : []));
    api.commands.list().then((r: any) => setHistory(Array.isArray(r) ? r : []));
    api.commands.saved.list().then((r: any) => setSaved(Array.isArray(r) ? r : []));
  };
  useEffect(() => {
    loadAll();
  }, []);

  useWs('command.output', (d: any) => {
    if (d.command_id !== activeId) return;
    setOutput((prev) => {
      const arr = prev[d.node_id] || [];
      return { ...prev, [d.node_id]: [...arr, { stream: d.stream, data: d.data }] };
    });
  });
  useWs('command.done', (d: any) => {
    if (d.command_id !== activeId) return;
    setOutput((prev) => {
      const arr = prev[d.node_id] || [];
      return { ...prev, [d.node_id]: [...arr, { stream: 'stderr', data: `\n[exit ${d.exit}]\n` }] };
    });
  });

  useEffect(() => {
    outRef.current?.scrollTo({ top: outRef.current.scrollHeight });
  }, [output]);

  const toggle = (id: string) =>
    setSelected((prev) => {
      const n = new Set(prev);
      n.has(id) ? n.delete(id) : n.add(id);
      return n;
    });

  const onlineIds = () => nodes.filter((n) => n.online || n.status === 'online').map((n) => n.id);
  const selectAll = () => setSelected(new Set(onlineIds()));
  const invert = () =>
    setSelected((prev) => new Set(nodes.filter((n) => !prev.has(n.id)).map((n) => n.id)));
  const clearAll = () => setSelected(new Set());

  const run = async (override?: string) => {
    const script = (override ?? cmd).trim();
    if (!script || selected.size === 0) {
      notify('请选择节点并输入命令', 'error');
      return;
    }
    setRunning(true);
    setOutput({});
    try {
      const r: any = await api.commands.run(Array.from(selected), script);
      setActiveId(r.id);
      setCmd(script);
      setHistory((p) => [{ id: r.id, cmd: script, status: 'running', created_at: Math.floor(Date.now() / 1000) }, ...p]);
    } catch (e: any) {
      notify(e?.response?.data?.error || '执行失败', 'error');
    } finally {
      setRunning(false);
    }
  };

  const saveCurrent = async () => {
    if (!cmd.trim()) {
      notify('命令为空', 'error');
      return;
    }
    const name = window.prompt('保存为常用命令，输入名称：', '');
    if (!name) return;
    try {
      const r: any = await api.commands.saved.create(name.trim(), cmd);
      setSaved((p) => [...p, r]);
      notify('已保存到常用命令', 'success');
    } catch (e: any) {
      notify(e?.response?.data?.error || '保存失败', 'error');
    }
  };

  const removeSaved = async (s: SavedCmd) => {
    if (s.builtin) return;
    if (!confirm(`删除常用命令「${s.name}」？`)) return;
    await api.commands.saved.remove(s.id);
    setSaved((p) => p.filter((x) => x.id !== s.id));
    notify('已删除');
  };

  const nodeName = (id: string) => nodes.find((n) => n.id === id)?.name || id.slice(0, 8);

  return (
    <div className="cmd-split">
      {/* left: composer */}
      <div className="card" style={{ padding: 16, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
        <div className="spread" style={{ marginBottom: 8 }}>
          <h3 style={{ margin: 0, fontSize: 15 }}>选择节点</h3>
          <div className="row" style={{ gap: 4 }}>
            <button className="btn sm ghost" title="全选在线节点" onClick={selectAll}>
              <CheckCheck size={13} /> 全选
            </button>
            <button className="btn sm ghost" title="反选" onClick={invert}>
              <Square size={13} /> 反选
            </button>
            <button className="btn sm ghost" title="清空" onClick={clearAll}>
              <X size={13} />
            </button>
          </div>
        </div>
        <div style={{ overflowY: 'auto', flex: 1, minHeight: 80, marginBottom: 12 }}>
          {nodes.length === 0 ? (
            <Empty text="无节点" />
          ) : (
            nodes.map((n) => (
              <label key={n.id} className="row" style={{ padding: '6px 6px', cursor: 'pointer', borderRadius: 6 }}>
                <input type="checkbox" checked={selected.has(n.id)} onChange={() => toggle(n.id)} />
                <span style={{ fontSize: 13 }}>
                  {n.name} <span style={{ color: 'var(--text-tertiary)' }}>· {n.online || n.status === 'online' ? '在线' : '离线'}</span>
                </span>
              </label>
            ))
          )}
        </div>

        {/* 常用命令 */}
        <div style={{ marginBottom: 10 }}>
          <div className="row" style={{ justifyContent: 'space-between', marginBottom: 6 }}>
            <span style={{ fontSize: 13, fontWeight: 600 }}>常用命令</span>
            <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>点击载入</span>
          </div>
          <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
            {saved.length === 0 && <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>（无）</span>}
            {saved.map((s) => (
              <div
                key={s.id}
                className="row"
                style={{
                  gap: 4,
                  padding: '3px 4px 3px 8px',
                  borderRadius: 999,
                  border: '1px solid var(--border-strong)',
                  background: s.builtin ? 'var(--bg-tertiary)' : 'var(--bg-primary)',
                  fontSize: 12,
                }}
              >
                <button
                  className="row"
                  title={s.builtin ? '安全加固预设' : s.name}
                  style={{ gap: 4, background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-primary)', padding: 0 }}
                  onClick={() => {
                    setCmd(s.script);
                    notify(`已载入「${s.name}」`, 'success');
                  }}
                >
                  {s.builtin && <Shield size={12} style={{ color: 'var(--primary)' }} />}
                  {s.name}
                </button>
                <button
                  title="在所选节点执行"
                  style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '0 2px', color: 'var(--primary)' }}
                  onClick={() => run(s.script)}
                >
                  <Play size={12} />
                </button>
                {!s.builtin && (
                  <button
                    title="删除"
                    style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '0 2px', color: 'var(--text-tertiary)' }}
                    onClick={() => removeSaved(s)}
                  >
                    <Trash2 size={11} />
                  </button>
                )}
              </div>
            ))}
          </div>
        </div>

        <div className="field">
          <label>命令（在所选节点上执行）</label>
          <textarea
            className="textarea"
            value={cmd}
            onChange={(e) => setCmd(e.target.value)}
            placeholder="例如：uname -a ; uptime"
            style={{ minHeight: 110 }}
          />
        </div>
        <div className="row" style={{ gap: 8 }}>
          <button className="btn primary" onClick={() => run()} disabled={running} style={{ flex: 1, justifyContent: 'center' }}>
            <Play size={15} /> {running ? '执行中…' : '执行'}
          </button>
          <button className="btn ghost" onClick={saveCurrent} title="保存当前命令到常用列表">
            <Save size={15} /> 保存
          </button>
        </div>
        <div className="row" style={{ marginTop: 10, justifyContent: 'space-between', alignItems: 'center' }}>
          <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
            <History size={12} style={{ verticalAlign: -2 }} /> 历史 {history.length}
          </span>
          <button
            className="btn sm ghost"
            onClick={() => api.commands.list().then((r: any) => setHistory(Array.isArray(r) ? r : []))}
          >
            刷新历史
          </button>
        </div>
      </div>

      {/* right: output */}
      <div className="card" style={{ padding: 16, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
        <h3 style={{ margin: '0 0 10px', fontSize: 15 }}>输出</h3>
        <div ref={outRef} style={{ flex: 1, overflowY: 'auto', background: 'var(--bg-secondary)', borderRadius: 8, padding: 12 }}>
          {Object.keys(output).length === 0 ? (
            <Empty text="执行命令后，输出将实时显示在此处" />
          ) : (
            Object.entries(output).map(([nid, lines]) => (
              <div key={nid} style={{ marginBottom: 14 }}>
                <div style={{ color: 'var(--primary)', fontWeight: 600, marginBottom: 4 }}>▶ {nodeName(nid)}</div>
                <pre className="mono" style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word', color: 'var(--text-secondary)' }}>
                  {lines.map((l) => l.data).join('')}
                </pre>
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  );
}
