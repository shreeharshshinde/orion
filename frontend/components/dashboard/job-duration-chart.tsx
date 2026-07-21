'use client';

import { ResponsiveContainer, LineChart, Line, XAxis, YAxis, Tooltip, CartesianGrid, Legend } from 'recharts';
import { DEMO_DURATION_HISTORY } from '@/lib/demo-data';

export function JobDurationChart() {
  return (
    <div className="relative rounded-xl border border-border/40 bg-card/45 p-5 backdrop-blur-md overflow-hidden w-full h-[280px] flex flex-col">
      <div className="flex items-center justify-between mb-4">
        <div>
          <h3 className="text-sm font-semibold text-text-bright">Execution Latency</h3>
          <p className="text-xs text-muted-foreground">Job duration percentile trends (ms)</p>
        </div>
      </div>

      <div className="flex-1 w-full h-full text-xs font-mono">
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={DEMO_DURATION_HISTORY} margin={{ top: 5, right: 5, left: -20, bottom: 0 }}>
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
            <Legend 
              verticalAlign="top" 
              height={36} 
              iconType="circle"
              iconSize={6}
              wrapperStyle={{
                fontFamily: 'var(--font-ui)',
                fontSize: '11px',
                paddingBottom: '10px'
              }}
            />
            <Line type="monotone" name="p50 (median)" dataKey="p50" stroke="var(--star)" strokeWidth={2} dot={false} activeDot={{ r: 4 }} />
            <Line type="monotone" name="p90 (tail)" dataKey="p90" stroke="var(--nebula)" strokeWidth={1.5} dot={false} strokeDasharray="3 3" />
            <Line type="monotone" name="p99 (extreme)" dataKey="p99" stroke="var(--collapse)" strokeWidth={1.5} dot={false} strokeDasharray="4 4" />
          </LineChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
