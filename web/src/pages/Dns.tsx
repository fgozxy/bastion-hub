import { useEffect, useState } from 'react';
import { Globe, RefreshCw, Plus, Pencil, Trash2, Save, ExternalLink, MoreVertical } from 'lucide-react';
import { api } from '../services/api';
import { notify } from '../stores';
import { Empty, Loading, Modal, ActionSheet, type ActionSheetItem } from '../components/ui';

type Zone = { id: string; name: string; status: string };
type DnsRecord = {
  id: string;
  type: string;
  name: string;
  content: string;
  ttl: number;
  proxied: boolean;
  priority?: number | null;
  comment?: string;
};

// Record types offered in the editor. Server validates against the same set.
const RECORD_TYPES = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'SRV', 'CAA', 'PTR', 'LOC', 'SSHFP', 'DS', 'TLSA'];
const TTL_OPTIONS = [1, 60, 300, 600, 1800, 3600, 7200, 10800, 21600, 43200, 86400];

const PROXIABLE = new Set(['A', 'AAAA', 'CNAME']);
const PRIORITY_TYPES = new Set(['MX', 'SRV']);

type Modal = { mode: 'add' } | { mode: 'edit'; rec: DnsRecord } | null;

export function DnsPage() {
  const [zones, setZones] = useState<Zone[]>([]);
  const [zoneId, setZoneId] = useState('');
  const [records, setRecords] = useState<DnsRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [noToken, setNoToken] = useState(false);
  const [modal, setModal] = useState<Modal>(null);
  const [busyId, setBusyId] = useState<string>('');
  const [sheet, setSheet] = useState<DnsRecord | null>(null);

  const loadZones = async () => {
    setNoToken(false);
    try {
      const r: any = await api.dns.zones();
      const zs: Zone[] = r?.zones || [];
      zs.sort((a, b) => a.name.localeCompare(b.name));
      setZones(zs);
      if (!zoneId && zs.length) setZoneId(zs[0].id);
      else if (zoneId && !zs.find((z) => z.id === zoneId)) setZoneId(zs[0]?.id || '');
    } catch (e: any) {
      const msg = e?.response?.data?.error || '';
      if (msg.includes('未配置 Cloudflare')) setNoToken(true);
      else notify(msg || '加载域名失败', 'error');
      setZones([]);
    }
  };

  const loadRecords = async (zid: string) => {
    if (!zid) {
      setRecords([]);
      return;
    }
    setLoading(true);
    try {
      const r: any = await api.dns.records(zid);
      const recs: DnsRecord[] = r?.records || [];
      recs.sort((a, b) => (a.type < b.type ? -1 : a.type > b.type ? 1 : a.name.localeCompare(b.name)));
      setRecords(recs);
    } catch (e: any) {
      notify(e?.response?.data?.error || '读取记录失败', 'error');
      setRecords([]);
    } finally {
      setLoading(false);
    }
  };

  // first mount: load zones (which seeds zoneId)
  useEffect(() => {
    (async () => {
      setLoading(true);
      await loadZones();
      setLoading(false);
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // whenever the selected zone changes, reload its records
  useEffect(() => {
    if (zoneId && !noToken) loadRecords(zoneId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [zoneId]);

  const reloadAll = async () => {
    await loadZones();
    if (zoneId) await loadRecords(zoneId);
  };

  const onDelete = async (rec: DnsRecord) => {
    if (!confirm(`删除 ${rec.type} 记录「${rec.name}」？\n内容：${rec.content}\n不可恢复。`)) return;
    setBusyId(rec.id);
    try {
      await api.dns.del(rec.id, zoneId);
      notify('已删除', 'success');
      loadRecords(zoneId);
    } catch (e: any) {
      notify(e?.response?.data?.error || '删除失败', 'error');
    } finally {
      setBusyId('');
    }
  };

  const zoneName = zones.find((z) => z.id === zoneId)?.name || '';

  return (
    <div>
      <div className="spread" style={{ marginBottom: 14, flexWrap: 'wrap', gap: 10 }}>
        <div>
          <h1 className="page-title">DNS</h1>
          <p className="page-subtitle" style={{ marginBottom: 0 }}>
            Cloudflare DNS 记录管理：对账号下任意域名（zone）的全部记录做新增 / 编辑 / 删除，支持 A / CNAME / MX / TXT / SRV … 等所有常用类型。改后立即生效。
          </p>
        </div>
        <div className="page-actions">
          <button className="btn ghost" onClick={reloadAll} disabled={loading || noToken}>
            <RefreshCw size={15} /> 刷新
          </button>
          <button className="btn primary" onClick={() => setModal({ mode: 'add' })} disabled={noToken || !zoneId}>
            <Plus size={15} /> 新增记录
          </button>
        </div>
      </div>

      {noToken && (
        <div className="card" style={{ padding: 18, maxWidth: 620 }}>
          <div className="row" style={{ gap: 10, marginBottom: 6 }}>
            <Globe size={18} color="var(--primary)" />
            <strong>未配置 Cloudflare 令牌</strong>
          </div>
          <p style={{ marginTop: 0, color: 'var(--text-secondary)' }}>
            DNS 板块需要 Cloudflare API token，且该 token 需对要管理的域名有 <code>Zone → DNS → Edit</code> 权限。
          </p>
          <a className="btn primary sm" href="/settings" style={{ textDecoration: 'none' }}>
            前往「设置 → Cloudflare」配置 <ExternalLink size={13} />
          </a>
        </div>
      )}

      {!noToken && (
        <>
          <div className="card" style={{ padding: 12, marginBottom: 12 }}>
            <div className="row" style={{ gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
              <label style={{ fontSize: 13, color: 'var(--text-secondary)' }}>域名 (zone)</label>
              <select
                className="select"
                style={{ minWidth: 240, flex: 1, maxWidth: 420 }}
                value={zoneId}
                onChange={(e) => setZoneId(e.target.value)}
              >
                {zones.length === 0 && <option value="">没有可管理的域名</option>}
                {zones.map((z) => (
                  <option key={z.id} value={z.id}>
                    {z.name} {z.status && z.status !== 'active' ? `（${z.status}）` : ''}
                  </option>
                ))}
              </select>
              {zoneName && (
                <span className="badge muted">
                  {records.length} 条记录
                </span>
              )}
            </div>
          </div>

          {loading && <Loading />}

          {!loading && records.length === 0 && <Empty text="该域名下没有 DNS 记录" />}

          {!loading && records.length > 0 && (
            <div className="card" style={{ padding: 0 }}>
              <div className="desktop-only">
              <div className="scroll-x">
                <table className="tbl">
                  <thead>
                    <tr>
                      <th>类型</th>
                      <th>名称</th>
                      <th>内容</th>
                      <th>代理</th>
                      <th>TTL</th>
                      <th style={{ textAlign: 'right' }}>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {records.map((rec) => (
                      <tr key={rec.id}>
                        <td>
                          <span className={`badge ${typeBadgeClass(rec.type)}`}>{rec.type}</span>
                          {rec.priority != null && (
                            <span className="mono" style={{ fontSize: 11, color: 'var(--text-tertiary)', marginLeft: 6 }}>
                              {rec.priority}
                            </span>
                          )}
                        </td>
                        <td>
                          <code className="mono" style={{ fontSize: 12 }}>{rec.name}</code>
                        </td>
                        <td>
                          <code className="mono" style={{ fontSize: 12 }} title={rec.content}>
                            {rec.content}
                          </code>
                        </td>
                        <td>
                          {rec.proxied ? (
                            <span className="badge success" title="已代理 (orange cloud)">已代理</span>
                          ) : (
                            <span className="badge muted" title="仅 DNS (DNS-only)">仅 DNS</span>
                          )}
                        </td>
                        <td style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                          {rec.ttl === 1 ? 'Auto' : `${rec.ttl}s`}
                        </td>
                        <td style={{ textAlign: 'right' }}>
                          <button
                            className="icon-btn"
                            title="编辑"
                            disabled={!!busyId}
                            onClick={() => setModal({ mode: 'edit', rec })}
                          >
                            <Pencil size={15} />
                          </button>
                          <button
                            className="icon-btn"
                            title="删除"
                            disabled={!!busyId}
                            onClick={() => onDelete(rec)}
                          >
                            <Trash2 size={15} />
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              </div>

              <div className="mobile-only m-list">
                {records.map((rec) => (
                  <div key={rec.id} className="m-item">
                    <div className="m-item-main" onClick={() => setSheet(rec)} role="button" tabIndex={0}
                      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSheet(rec); } }}>
                      <div className="m-item-title" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                        <span className={`badge ${typeBadgeClass(rec.type)}`}>{rec.type}</span>
                        <span className="mono text-ellipsis" style={{ flex: 1 }}>{rec.name}</span>
                      </div>
                      <div className="m-item-sub mono">{rec.content}</div>
                      <div className="m-item-meta">
                        {rec.proxied ? (
                          <span className="badge success">已代理</span>
                        ) : (
                          <span className="badge muted">仅 DNS</span>
                        )}
                        <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
                          TTL {rec.ttl === 1 ? 'Auto' : `${rec.ttl}s`}
                        </span>
                      </div>
                    </div>
                    <button className="icon-btn m-item-more" title="更多" onClick={() => setSheet(rec)}>
                      <MoreVertical size={18} />
                    </button>
                  </div>
                ))}
              </div>

              <ActionSheet
                open={!!sheet}
                onClose={() => setSheet(null)}
                title={sheet ? `${sheet.type} · ${sheet.name}` : undefined}
                actions={sheet ? ([
                  {
                    key: 'edit',
                    label: '编辑记录',
                    icon: <Pencil size={18} />,
                    disabled: !!busyId,
                    onClick: () => setModal({ mode: 'edit', rec: sheet }),
                  },
                  {
                    key: 'delete',
                    label: '删除记录',
                    icon: <Trash2 size={18} />,
                    danger: true,
                    disabled: !!busyId,
                    onClick: () => onDelete(sheet),
                  },
                ] as ActionSheetItem[]) : []}
              >
                {sheet && (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                    <div className="kv-row">
                      <span>内容</span>
                      <code className="mono break-anywhere">{sheet.content}</code>
                    </div>
                    <div className="kv-row">
                      <span>代理</span>
                      <span>{sheet.proxied ? '已代理' : '仅 DNS'}</span>
                    </div>
                    <div className="kv-row">
                      <span>TTL</span>
                      <span>{sheet.ttl === 1 ? 'Auto' : `${sheet.ttl}s`}</span>
                    </div>
                    {sheet.priority != null && (
                      <div className="kv-row">
                        <span>优先级</span>
                        <span className="mono">{sheet.priority}</span>
                      </div>
                    )}
                  </div>
                )}
              </ActionSheet>
            </div>
          )}
        </>
      )}

      {modal && (
        <RecordModal
          mode={modal.mode}
          zoneId={zoneId}
          zoneName={zoneName}
          rec={modal.mode === 'edit' ? modal.rec : undefined}
          onClose={() => setModal(null)}
          onDone={() => {
            setModal(null);
            notify('已保存', 'success');
            loadRecords(zoneId);
          }}
        />
      )}
    </div>
  );
}

function typeBadgeClass(t: string): string {
  switch (t) {
    case 'A':
    case 'AAAA':
      return 'success';
    case 'CNAME':
      return 'success';
    case 'MX':
      return 'warning';
    case 'TXT':
      return 'muted';
    case 'NS':
      return 'warning';
    case 'SRV':
      return 'warning';
    default:
      return 'muted';
  }
}

function contentHint(t: string): string {
  switch (t) {
    case 'A':
      return 'IPv4 地址，如 192.0.2.1';
    case 'AAAA':
      return 'IPv6 地址，如 2001:db8::1';
    case 'CNAME':
      return '目标主机名，如 example.com.';
    case 'MX':
      return '邮件服务器主机名（优先级单独填），如 mail.example.com.';
    case 'TXT':
      return '任意文本，常用作 SPF / 验证。多条语义请合并为一条。';
    case 'NS':
      return '权威 NS 服务器，如 ns1.example.com.';
    case 'SRV':
      return '权重 端口 目标，如 1 443 _sip._tls.example.com.';
    case 'CAA':
      return 'flags tag value，如 0 issue letsencrypt.org';
    default:
      return '记录内容';
  }
}

function RecordModal({
  mode,
  zoneId,
  zoneName,
  rec,
  onClose,
  onDone,
}: {
  mode: 'add' | 'edit';
  zoneId: string;
  zoneName: string;
  rec?: DnsRecord;
  onClose: () => void;
  onDone: () => void;
}) {
  const [type, setType] = useState(rec?.type || 'A');
  const [name, setName] = useState(rec?.name || '');
  const [content, setContent] = useState(rec?.content || '');
  const [ttl, setTtl] = useState(rec?.ttl || 1);
  const [proxied, setProxied] = useState(rec?.proxied || false);
  const [priority, setPriority] = useState<string>(rec?.priority != null ? String(rec.priority) : '');
  const [comment, setComment] = useState(rec?.comment || '');
  const [saving, setSaving] = useState(false);

  const canProxy = PROXIABLE.has(type);
  const usePriority = PRIORITY_TYPES.has(type);
  // effective proxied: non-proxiable types can never be proxied
  const effProxied = canProxy && proxied;

  const save = async () => {
    if (!name.trim()) return notify('名称不能为空', 'error');
    if (!content.trim()) return notify('内容不能为空', 'error');
    setSaving(true);
    try {
      const prio = usePriority && priority.trim() !== '' ? Number(priority) : null;
      if (mode === 'add') {
        await api.dns.create({
          zone_id: zoneId,
          type,
          name: name.trim(),
          content: content.trim(),
          ttl,
          proxied: effProxied,
          priority: prio,
          comment: comment.trim(),
        });
      } else if (rec) {
        await api.dns.update(rec.id, zoneId, {
          type,
          name: name.trim(),
          content: content.trim(),
          ttl,
          proxied: effProxied,
          priority: prio,
          comment: comment.trim(),
        });
      }
      onDone();
    } catch (e: any) {
      notify(e?.response?.data?.error || '保存失败', 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={mode === 'add' ? `新增 DNS 记录 — ${zoneName}` : `编辑 ${rec?.type} 记录 — ${zoneName}`}
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
        <label>类型</label>
        <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
          {RECORD_TYPES.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
      </div>

      <div className="field">
        <label>名称</label>
        <input
          className="input mono"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={`如 @ 或 www（CF 接受短名或全名，@ = ${zoneName || 'zone 根'}）`}
          autoFocus
        />
      </div>

      <div className="field">
        <label>内容</label>
        <textarea
          className="input mono"
          value={content}
          onChange={(e) => setContent(e.target.value)}
          placeholder={contentHint(type)}
          rows={3}
          style={{ resize: 'vertical', fontFamily: 'var(--mono-font, monospace)' }}
        />
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>{contentHint(type)}</div>
      </div>

      <div className="row" style={{ gap: 16, flexWrap: 'wrap' }}>
        <div className="field" style={{ flex: '1 1 160px', minWidth: 160 }}>
          <label>TTL</label>
          <select className="select" value={ttl} onChange={(e) => setTtl(Number(e.target.value))} disabled={effProxied}>
            {TTL_OPTIONS.map((t) => (
              <option key={t} value={t}>
                {t === 1 ? 'Auto' : `${t}s`}
              </option>
            ))}
          </select>
          {effProxied && (
            <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>已代理时 TTL 固定 Auto</div>
          )}
        </div>

        {usePriority && (
          <div className="field" style={{ flex: '1 1 140px', minWidth: 140 }}>
            <label>优先级</label>
            <input
              className="input"
              type="number"
              value={priority}
              onChange={(e) => setPriority(e.target.value)}
              placeholder="如 10"
            />
          </div>
        )}
      </div>

      <div className="field">
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: canProxy ? 'pointer' : 'not-allowed' }}>
          <input
            type="checkbox"
            checked={effProxied}
            onChange={(e) => setProxied(e.target.checked)}
            disabled={!canProxy}
          />
          <span>
            已代理 (orange cloud)
            {!canProxy && <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}> — 该类型不可代理</span>}
          </span>
        </label>
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginTop: 4 }}>
          勾选后流量经 Cloudflare 代理（隐藏源站 IP、享受 CF 的 HTTPS / 缓存 / WAF）；仅 A / AAAA / CNAME 可代理。
        </div>
      </div>

      <div className="field">
        <label>备注 (可选)</label>
        <input className="input" value={comment} onChange={(e) => setComment(e.target.value)} placeholder="记录用途说明" />
      </div>
    </Modal>
  );
}
