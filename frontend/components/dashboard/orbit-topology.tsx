'use client';

import { useEffect, useState } from 'react';
import { motion, useReducedMotion } from 'framer-motion';
import { Activity, Server, Waypoints } from 'lucide-react';

interface OrbitNode {
  id: string;
  name: string;
  icon: typeof Activity;
  url: string;
  angleOffset: number; // in radians
}

export function OrbitTopology({
  apiConnected = true,
  schedulerActive = true,
  workerCount = 3,
}) {
  const shouldReduceMotion = useReducedMotion();
  const [pulseLine1, setPulseLine1] = useState(false);
  const [pulseLine2, setPulseLine2] = useState(false);

  // Trigger random transmission pulses between nodes for visual telemetry
  useEffect(() => {
    if (shouldReduceMotion) return;
    const interval = setInterval(() => {
      if (Math.random() > 0.5) {
        setPulseLine1(true);
        setTimeout(() => setPulseLine1(false), 1200);
      } else {
        setPulseLine2(true);
        setTimeout(() => setPulseLine2(false), 1200);
      }
    }, 4000);
    return () => clearInterval(interval);
  }, [shouldReduceMotion]);

  // Orbit parameters
  const center = { x: 150, y: 150 };
  const radius = 80;

  // Nodes definition
  const nodes: OrbitNode[] = [
    { id: 'api', name: 'API Server', icon: Activity, url: '/docs', angleOffset: 0 },
    { id: 'scheduler', name: 'Scheduler', icon: Waypoints, url: '/dashboard/queues', angleOffset: (2 * Math.PI) / 3 },
    { id: 'worker', name: 'Workers', icon: Server, url: '/dashboard/workers', angleOffset: (4 * Math.PI) / 3 },
  ];

  return (
    <div className="relative flex flex-col items-center justify-center rounded-xl border border-border/40 bg-card/45 p-6 backdrop-blur-md overflow-hidden h-[300px] w-full">
      {/* Background Starfield Effect */}
      <div className="absolute inset-0 pointer-events-none opacity-25">
        <div className="absolute top-12 left-1/4 w-1 h-1 bg-white rounded-full animate-pulse" />
        <div className="absolute top-2/3 left-12 w-1.5 h-1.5 bg-cyan-400 rounded-full animate-ping [animation-duration:4s]" />
        <div className="absolute top-1/3 right-16 w-1 h-1 bg-violet-400 rounded-full animate-pulse" />
        <div className="absolute bottom-8 right-1/3 w-1.5 h-1.5 bg-white rounded-full" />
      </div>

      <h3 className="absolute top-4 left-5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        Live Topology Orbit
      </h3>

      <div className="relative w-[300px] h-[300px]">
        {/* SVG for connections and orbit path */}
        <svg className="absolute inset-0 w-full h-full" viewBox="0 0 300 300">
          {/* Orbit paths */}
          {!shouldReduceMotion && (
            <circle
              cx={center.x}
              cy={center.y}
              r={radius}
              fill="none"
              stroke="var(--panel-edge)"
              strokeWidth="1"
              strokeDasharray="4 4"
            />
          )}

          {/* Connection Lines (Triangular Web) */}
          <line
            x1={center.x + radius * Math.cos(0)}
            y1={center.y + radius * Math.sin(0)}
            x2={center.x + radius * Math.cos((2 * Math.PI) / 3)}
            y2={center.y + radius * Math.sin((2 * Math.PI) / 3)}
            stroke={pulseLine1 && apiConnected ? 'var(--star)' : 'var(--panel-edge)'}
            strokeWidth={pulseLine1 && apiConnected ? '2' : '1.2'}
            className="transition-colors duration-500"
          />
          <line
            x1={center.x + radius * Math.cos((2 * Math.PI) / 3)}
            y1={center.y + radius * Math.sin((2 * Math.PI) / 3)}
            x2={center.x + radius * Math.cos((4 * Math.PI) / 3)}
            y2={center.y + radius * Math.sin((4 * Math.PI) / 3)}
            stroke={pulseLine2 && schedulerActive ? 'var(--nebula)' : 'var(--panel-edge)'}
            strokeWidth={pulseLine2 && schedulerActive ? '2' : '1.2'}
            className="transition-colors duration-500"
          />
          <line
            x1={center.x + radius * Math.cos((4 * Math.PI) / 3)}
            y1={center.y + radius * Math.sin((4 * Math.PI) / 3)}
            x2={center.x + radius * Math.cos(0)}
            y2={center.y + radius * Math.sin(0)}
            stroke={apiConnected && workerCount > 0 ? 'var(--star)' : 'var(--panel-edge)'}
            strokeWidth="1.2"
            opacity={0.6}
          />

          {/* Data transmission packet animation */}
          {pulseLine1 && !shouldReduceMotion && (
            <motion.circle
              r="4"
              fill="var(--star)"
              filter="drop-shadow(0 0 4px var(--star))"
              animate={{
                cx: [
                  center.x + radius * Math.cos(0),
                  center.x + radius * Math.cos((2 * Math.PI) / 3),
                ],
                cy: [
                  center.y + radius * Math.sin(0),
                  center.y + radius * Math.sin((2 * Math.PI) / 3),
                ],
              }}
              transition={{ duration: 1.2, ease: 'easeInOut' }}
            />
          )}

          {pulseLine2 && !shouldReduceMotion && (
            <motion.circle
              r="4"
              fill="var(--nebula)"
              filter="drop-shadow(0 0 4px var(--nebula))"
              animate={{
                cx: [
                  center.x + radius * Math.cos((2 * Math.PI) / 3),
                  center.x + radius * Math.cos((4 * Math.PI) / 3),
                ],
                cy: [
                  center.y + radius * Math.sin((2 * Math.PI) / 3),
                  center.y + radius * Math.sin((4 * Math.PI) / 3),
                ],
              }}
              transition={{ duration: 1.2, ease: 'easeInOut' }}
            />
          )}
        </svg>

        {/* Orbiting bodies */}
        {nodes.map((node) => {
          const Icon = node.icon;

          // If reduced motion is requested, use static layout
          if (shouldReduceMotion) {
            const x = center.x + radius * Math.cos(node.angleOffset);
            const y = center.y + radius * Math.sin(node.angleOffset);
            return (
              <div
                key={node.id}
                style={{
                  position: 'absolute',
                  left: x,
                  top: y,
                  transform: 'translate(-50%, -50%)',
                }}
                className="group flex flex-col items-center gap-1 cursor-pointer z-10"
              >
                <div className="flex h-11 w-11 items-center justify-center rounded-full border border-panel-edge bg-panel-solid text-muted-foreground shadow-lg transition-all group-hover:border-primary/50 group-hover:text-primary">
                  <Icon className="h-5 w-5" />
                </div>
                <span className="text-[10px] font-medium font-ui text-muted-foreground uppercase tracking-wider bg-void/80 px-1 rounded">
                  {node.name}
                </span>
              </div>
            );
          }

          // Otherwise, orbit continuously!
          return (
            <motion.div
              key={node.id}
              className="absolute group flex flex-col items-center gap-1 cursor-pointer z-10"
              style={{
                left: center.x,
                top: center.y,
              }}
              animate={{
                transform: [
                  `translate(-50%, -50%) rotate(${node.angleOffset}rad) translate(${radius}px) rotate(-${node.angleOffset}rad)`,
                  `translate(-50%, -50%) rotate(${node.angleOffset + 2 * Math.PI}rad) translate(${radius}px) rotate(-${node.angleOffset + 2 * Math.PI}rad)`,
                ],
              }}
              transition={{
                duration: 20,
                repeat: Infinity,
                ease: 'linear',
              }}
            >
              <div className="flex h-11 w-11 items-center justify-center rounded-full border border-panel-edge bg-panel-solid text-muted-foreground shadow-lg transition-all group-hover:border-primary/50 group-hover:text-primary relative">
                <Icon className="h-5 w-5" />
                {node.id === 'api' && apiConnected && (
                  <span className="absolute -top-0.5 -right-0.5 flex h-2.5 w-2.5">
                    <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"></span>
                    <span className="relative inline-flex rounded-full h-2.5 w-2.5 bg-emerald-500"></span>
                  </span>
                )}
                {node.id === 'scheduler' && schedulerActive && (
                  <span className="absolute -top-0.5 -right-0.5 flex h-2.5 w-2.5">
                    <span className="relative inline-flex rounded-full h-2.5 w-2.5 bg-cyan-400"></span>
                  </span>
                )}
              </div>
              <span className="text-[9px] font-medium font-ui text-muted-foreground/60 group-hover:text-muted-foreground transition-colors uppercase tracking-wider bg-void/80 px-1.5 py-0.5 rounded border border-border/30">
                {node.name}
              </span>
            </motion.div>
          );
        })}

        {/* Constellation Belt Indicator Center */}
        <div className="absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 flex h-8 w-8 items-center justify-center rounded-full bg-primary/5 border border-primary/20">
          <div className="h-2 w-2 rounded-full bg-primary animate-ping" />
        </div>
      </div>
    </div>
  );
}
