import { useEffect, useRef, useState } from 'react';
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  BarElement,
  ArcElement,
  Tooltip,
  Legend,
  Filler,
} from 'chart.js';
import { Doughnut, Bar, Line } from 'react-chartjs-2';
import { Server, TerminalSquare, KeyRound, Archive } from 'lucide-react';
import { api } from '../services/api';
import { useWs } from '../hooks/useWs';
import { flagEmoji, relTime } from '../lib/utils';

ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  BarElement,
  ArcElement,
  Tooltip,
  Legend,
  Filler
);

const accent = '#8b8680';
const success = '#10b981';
const warning = '#c65746';
const muted = '#a29c95';

export function DashboardPage() {
  const [stats, setStats] = useState<any>(null);
  const cpuRef = useRef<{ t: number; v: number }[]>([]);

  const load = () => api.dashboard().then(setStats).catch(() => {});
  useEffect(() => {
    load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, []);

  // live aggregate CPU from metrics events
  const latest = useRef<Record<string, any>>({});
  useWs('node.metrics', (data: any) => {
    latest.current[data.id] = data;
    const vals = Object.values(latest.current);
    const agg = vals.reduce((s: number, m: any) => s + (m.cpu || 0), 0) / Math.max(1, vals.length);
    pushCpu(agg);
  });

  function pushCpu(v: number) {
    const now = Math.floor(Date.now() / 1000);
    cpuRef.current.push({ t: now, v: Math.round(v * 10) / 10 });
    if (cpuRef.current.length > 30) cpuRef.current.shift();
    setCpuTick((x) => x + 1);
  }
  const [, setCpuTick] = useState(0);

  const s = stats || {};
  const nodes = s.nodes || { total: 0, online: 0 };
  const online = nodes.online || 0;
  const offline = (nodes.total || 0) - online;

  const cards = [
    { label: '节点', value: `${online}/${nodes.total}`, sub: '在线/总数', icon: Server },
    { label: '今日命令', value: s.commands?.today ?? '-', sub: `累计 ${s.commands?.total ?? 0}`, icon: TerminalSquare },
    { label: '凭证', value: s.credentials ?? '-', sub: 'SSH 密钥', icon: KeyRound },
    { label: '备份成功', value: s.backups?.success ?? '-', sub: `失败 ${s.backups?.failed ?? 0}`, icon: Archive },
  ];

  const countries: any[] = s.countries || [];

  return (
    <div>
      <h1 className="page-title">仪表盘</h1>
      <p className="page-subtitle">面板运行概览（实时）</p>

      <div className="auto-grid" style={{ '--grid-min': '200px', marginBottom: 18 } as any}>
        {cards.map((c, i) => (
          <div className="card" key={i} style={{ padding: 16, display: 'flex', gap: 12, alignItems: 'center' }}>
            <div style={{ background: 'var(--bg-tertiary)', borderRadius: 10, width: 42, height: 42, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--primary)' }}>
              <c.icon size={20} />
            </div>
            <div>
              <div style={{ fontSize: 22, fontWeight: 700 }}>{c.value}</div>
              <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                {c.label} · {c.sub}
              </div>
            </div>
          </div>
        ))}
      </div>

      <div className="auto-grid" style={{ '--grid-min': '320px' } as any}>
        <div className="card" style={{ padding: 18 }}>
          <h3 style={{ margin: '0 0 10px', fontSize: 15 }}>节点在线状态</h3>
          <div style={{ height: 220 }}>
            <Doughnut
              data={{
                labels: ['在线', '离线'],
                datasets: [{ data: [online, offline], backgroundColor: [success, muted], borderWidth: 0 }],
              }}
              options={{ maintainAspectRatio: false, cutout: '65%' }}
            />
          </div>
        </div>

        <div className="card" style={{ padding: 18 }}>
          <h3 style={{ margin: '0 0 10px', fontSize: 15 }}>节点地理分布</h3>
          <div style={{ height: 220 }}>
            {countries.length ? (
              <Bar
                data={{
                  labels: countries.map((c) => `${flagEmoji(c.code)} ${c.code}`),
                  datasets: [{ label: '节点数', data: countries.map((c) => c.count), backgroundColor: accent }],
                }}
                options={{ maintainAspectRatio: false, plugins: { legend: { display: false } } }}
              />
            ) : (
              <EmptyChart />
            )}
          </div>
        </div>

        <div className="card" style={{ padding: 18 }}>
          <h3 style={{ margin: '0 0 10px', fontSize: 15 }}>CPU 负载（实时聚合）</h3>
          <div style={{ height: 220 }}>
            <Line
              data={{
                labels: cpuRef.current.map((p) => ''),
                datasets: [
                  {
                    data: cpuRef.current.map((p) => p.v),
                    borderColor: accent,
                    backgroundColor: 'rgba(139,134,128,0.15)',
                    fill: true,
                    tension: 0.35,
                    pointRadius: 0,
                  },
                ],
              }}
              options={{
                maintainAspectRatio: false,
                plugins: { legend: { display: false } },
                scales: { y: { beginAtZero: true, max: 100, ticks: { callback: (v) => v + '%' } }, x: { display: false } },
              }}
            />
          </div>
        </div>

        <div className="card" style={{ padding: 18 }}>
          <h3 style={{ margin: '0 0 10px', fontSize: 15 }}>最近备份</h3>
          <div style={{ height: 220 }}>
            {(s.backups?.recent || []).length ? (
              <Bar
                data={{
                  labels: (s.backups.recent || []).map((b: any) => relTime(b.created_at)),
                  datasets: [
                    {
                      label: '大小',
                      data: (s.backups.recent || []).map((b: any) => (b.size || 0) / 1024 / 1024),
                      backgroundColor: (s.backups.recent || []).map((b: any) => (b.status === 'ok' ? success : b.status === 'failed' ? warning : muted)),
                    },
                  ],
                }}
                options={{ maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: { y: { ticks: { callback: (v) => v + 'M' } } } }}
              />
            ) : (
              <EmptyChart />
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function EmptyChart() {
  return <div style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-tertiary)' }}>暂无数据</div>;
}
