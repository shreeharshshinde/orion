'use client';

import { ResponsiveContainer, AreaChart, Area, XAxis, YAxis, Tooltip, CartesianGrid } from 'recharts';
import { DEMO_QUEUE_DEPTH_HISTORY } from '@/lib/demo-data';

export function QueueDepthChart() {
  return (
    <div className="relative rounded-xl border border-border/40 bg-card/45 p-5 backdrop-blur-md overflow-hidden w-full h-[280px] flex flex-col">
      <div className="flex items-center justify-between mb-4">
        <div>
          <h3 className="text-sm font-semibold text-text-bright">Queue Pressure</h3>
          <p className="text-xs text-muted-foreground">Queue depth historical logs (24h)</p>
        </div>
        <div className="flex items-center gap-3 text-[10px] font-mono">
          <span className="flex items-center gap-1"><span className="h-2 w-2 rounded bg-amber-400" /> high</span>
          <span className="flex items-center gap-1"><span className="h-2 w-2 rounded bg-cyan-400" /> default</span>
          <span className="flex items-center gap-1"><span className="h-2 w-2 rounded bg-violet-400" /> low</span>
        </div>
      </div>

      <div className="flex-1 w-full h-full text-xs font-mono">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={DEMO_QUEUE_DEPTH_HISTORY} margin={{ top: 5, right: 5, left: -25, bottom: 0 }}>
            <defs>
              <linearGradient id="colorHigh" x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor="#fbbf66" stopOpacity={0.25} />
                <stop offset="95%" stopColor="#fbbf66" stopOpacity={0} />
              </linearGradient>
              <linearGradient id="colorDefault" x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor="#22d3ee" stopOpacity={0.25} />
                <stop offset="95%" stopColor="#22d3ee" stopOpacity={0} />
              </linearGradient>
              <linearGradient id="colorLow" x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor="#8b7fe8" stopOpacity={0.2} />
                <stop offset="95%" stopColor="#8b7fe8" stopOpacity={0} />
              </linearGradient>
            </defs>
            <CartesianGrid stroke="var(--panel-edge)" vertical={false} strokeDasharray="3 3" />
            <XAxis dataKey="timestamp" stroke="var(--text-faint)" dy={5} tickLine={false} />
            <YAxis stroke="var(--text-faint)" dx={-5} tickLine={false} />
            <Tooltip
              contentStyle={{
                backgroundColor: 'var(--panel-solid)',
                borderColor: 'var(--panel-edge)',
                borderRadius: '8px',
                color: 'var(--text-bright)',
                fontFamily: 'var(--font-data)',
                fontSize: '11px',
              }}
            />
            <Area type="monotone" dataKey="high" stroke="#fbbf66" fillOpacity={1} fill="url(#colorHigh)" strokeWidth={1.5} stackId="1" />
            <Area type="monotone" dataKey="default" stroke="#22d3ee" fillOpacity={1} fill="url(#colorDefault)" strokeWidth={1.5} stackId="1" />
            <Area type="monotone" dataKey="low" stroke="#8b7fe8" fillOpacity={1} fill="url(#colorLow)" strokeWidth={1.5} stackId="1" />
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
