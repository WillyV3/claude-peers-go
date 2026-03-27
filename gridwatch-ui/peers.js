// ========== PAGE 5: PEER NETWORK GRAPH ==========
// Constellation map of the fleet. Machines as nodes, Claude instances as orbiting satellites.

const GRAPH_NODES = [
  { id: 'ubuntu-homelab', x: 400, y: 70,  role: 'HUB' },
  { id: 'omarchy',        x: 170, y: 150 },
  { id: 'sonias-mbp',     x: 635, y: 125, role: 'LLM' },
  { id: 'thinkbook',      x: 125, y: 300 },
  { id: 'macbook1',       x: 575, y: 275 },
  { id: 'raspdeck',       x: 310, y: 345, role: 'KIOSK' },
  { id: 'willyv4',        x: 495, y: 350, role: 'DECK' },
];

const GRAPH_NAMES = {
  'omarchy': 'OMARCHY',
  'ubuntu-homelab': 'U-HOMELAB',
  'macbook1': 'MACBOOK',
  'sonias-mbp': 'SONIA-MBP',
  'raspdeck': 'RASPDECK',
  'willyv4': 'WILLYV4',
  'thinkbook': 'THINKBOOK',
};

let peerCanvas, peerCtx;
let graphRunning = false;
let graphStart = 0;

// Data fed from core.js pollAll.
let graphMachines = {};
let graphPeers = [];

function updatePeersPage(machines, peers) {
  graphMachines = machines || {};
  graphPeers = peers || [];
}

function startPeerGraph() {
  if (graphRunning) return;
  peerCanvas = document.getElementById('peer-canvas');
  if (!peerCanvas) return;
  const c = peerCanvas.parentElement;
  peerCanvas.width = c.clientWidth;
  peerCanvas.height = c.clientHeight;
  peerCtx = peerCanvas.getContext('2d');
  graphRunning = true;
  graphStart = performance.now();
  requestAnimationFrame(graphFrame);
}

function stopPeerGraph() {
  graphRunning = false;
}

function graphFrame(now) {
  if (!graphRunning) return;
  drawGraph(peerCtx, peerCanvas.width, peerCanvas.height, (now - graphStart) / 1000);
  requestAnimationFrame(graphFrame);
}

function hexRgba(hex, a) {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  return `rgba(${r},${g},${b},${a})`;
}

function drawGraph(ctx, w, h, t) {
  ctx.fillStyle = '#0B0C16';
  ctx.fillRect(0, 0, w, h);

  // Dot grid
  ctx.fillStyle = 'rgba(26,28,48,0.6)';
  for (let x = 20; x < w; x += 30)
    for (let y = 15; y < h; y += 30)
      ctx.fillRect(x, y, 1, 1);

  // Group peers by machine
  const byMachine = {};
  for (const p of graphPeers) {
    const mid = normalizeMachine(p.machine);
    if (!byMachine[mid]) byMachine[mid] = [];
    byMachine[mid].push(p);
  }

  // Build node states
  const nodes = GRAPH_NODES.map(n => {
    const m = graphMachines[n.id];
    const online = m && m.status === 'online';
    const agents = byMachine[n.id] || [];
    const active = agents.filter(a =>
      !a.last_seen || (Date.now() - new Date(a.last_seen).getTime()) <= 60000
    );
    const baseR = 16;
    const r = baseR + Math.min(active.length * 3, 12);
    const breathe = Math.sin(t * 0.8 + n.x * 0.01) * 1.5;
    const dx = Math.sin(t * 0.15 + n.y * 0.02) * 3;
    const dy = Math.cos(t * 0.12 + n.x * 0.02) * 2;
    return { ...n, online, agents, active, r: r + breathe, dx: n.x + dx, dy: n.y + dy };
  });

  drawConnections(ctx, nodes, t);
  for (const ns of nodes) drawNode(ctx, ns, t);
  for (const ns of nodes) drawSatellites(ctx, ns, t, w);
  drawTitle(ctx, w, byMachine);
}

function drawConnections(ctx, nodes, t) {
  const on = nodes.filter(n => n.online);
  for (let i = 0; i < on.length; i++) {
    for (let j = i + 1; j < on.length; j++) {
      const a = on[i], b = on[j];
      const hub = a.role === 'HUB' || b.role === 'HUB';
      const busy = a.active.length > 0 && b.active.length > 0;
      const alpha = hub ? (busy ? 0.18 : 0.08) : (busy ? 0.06 : 0.03);

      ctx.beginPath();
      ctx.moveTo(a.dx, a.dy);
      ctx.lineTo(b.dx, b.dy);
      ctx.strokeStyle = hub
        ? `rgba(130,251,156,${alpha})`
        : `rgba(106,110,149,${alpha})`;
      ctx.lineWidth = hub ? 1 : 0.5;
      ctx.stroke();

      // Flowing particles on hub + busy connections
      if (hub && busy) {
        const spd = 0.15 + i * 0.03;
        for (const off of [0, 0.5]) {
          const pct = ((t * spd + i * 0.4 + off) % 1 + 1) % 1;
          const px = a.dx + (b.dx - a.dx) * pct;
          const py = a.dy + (b.dy - a.dy) * pct;
          const g = ctx.createRadialGradient(px, py, 0, px, py, 5);
          g.addColorStop(0, 'rgba(130,251,156,0.5)');
          g.addColorStop(1, 'rgba(130,251,156,0)');
          ctx.fillStyle = g;
          ctx.fillRect(px - 5, py - 5, 10, 10);
        }
      }
    }
  }
}

function drawNode(ctx, ns, t) {
  const { dx: x, dy: y, r, online, active, id, role } = ns;
  const hasAgents = active.length > 0;
  const color = online ? (hasAgents ? '#82FB9C' : '#50f872') : '#ff6b6b';
  const ga = online ? (0.12 + Math.sin(t * 0.6 + ns.x * 0.01) * 0.06) : 0.04;

  // Glow
  const glow = ctx.createRadialGradient(x, y, r * 0.3, x, y, r * 2.5);
  glow.addColorStop(0, hexRgba(color, ga));
  glow.addColorStop(1, hexRgba(color, 0));
  ctx.fillStyle = glow;
  ctx.beginPath();
  ctx.arc(x, y, r * 2.5, 0, Math.PI * 2);
  ctx.fill();

  // Fill
  ctx.beginPath();
  ctx.arc(x, y, r, 0, Math.PI * 2);
  ctx.fillStyle = '#0f1019';
  ctx.fill();

  // Border
  ctx.beginPath();
  ctx.arc(x, y, r, 0, Math.PI * 2);
  ctx.strokeStyle = hexRgba(color, online ? 0.6 : 0.2);
  ctx.lineWidth = online ? 1.5 : 0.8;
  ctx.stroke();

  // Agent ring
  if (hasAgents) {
    ctx.beginPath();
    ctx.arc(x, y, r - 3, 0, Math.PI * 2);
    ctx.strokeStyle = 'rgba(217,119,87,0.3)';
    ctx.lineWidth = 1;
    ctx.stroke();
  }

  // Name
  ctx.font = '600 10px "Monaspace Krypton",monospace';
  ctx.textAlign = 'center';
  ctx.fillStyle = online ? '#82FB9C' : 'rgba(106,110,149,0.5)';
  ctx.fillText(GRAPH_NAMES[id] || id, x, y + r + 14);

  // Role
  if (role) {
    ctx.font = '600 7px "Monaspace Krypton",monospace';
    ctx.fillStyle = 'rgba(106,110,149,0.7)';
    ctx.fillText(role, x, y - r - 6);
  }

  // Count or dot
  if (hasAgents) {
    ctx.font = '700 11px "Monaspace Krypton",monospace';
    ctx.fillStyle = '#D97757';
    ctx.fillText(active.length, x, y + 4);
  } else if (online) {
    ctx.beginPath();
    ctx.arc(x, y, 2, 0, Math.PI * 2);
    ctx.fillStyle = 'rgba(80,248,114,0.4)';
    ctx.fill();
  }
}

function drawSatellites(ctx, ns, t, canvasW) {
  const { dx: cx, dy: cy, r, agents } = ns;
  if (!agents.length) return;

  const orbitR = r + 16;
  agents.forEach((agent, i) => {
    const stale = agent.last_seen && (Date.now() - new Date(agent.last_seen).getTime()) > 60000;
    const base = (i / Math.max(agents.length, 1)) * Math.PI * 2;
    const angle = base + t * (stale ? 0.05 : 0.2);
    const ax = cx + Math.cos(angle) * orbitR;
    const ay = cy + Math.sin(angle) * orbitR;
    const sr = stale ? 2.5 : 3.5;

    // Glow
    if (!stale) {
      const sg = ctx.createRadialGradient(ax, ay, 0, ax, ay, 8);
      sg.addColorStop(0, 'rgba(217,119,87,0.25)');
      sg.addColorStop(1, 'rgba(217,119,87,0)');
      ctx.fillStyle = sg;
      ctx.fillRect(ax - 8, ay - 8, 16, 16);
    }

    // Dot
    ctx.beginPath();
    ctx.arc(ax, ay, sr, 0, Math.PI * 2);
    ctx.fillStyle = stale ? 'rgba(106,110,149,0.4)' : '#D97757';
    ctx.fill();

    // Trail
    if (!stale) {
      ctx.beginPath();
      ctx.arc(cx, cy, orbitR, angle - 0.5, angle, false);
      ctx.strokeStyle = 'rgba(217,119,87,0.08)';
      ctx.lineWidth = 1;
      ctx.stroke();
    }

    // Label
    const label = agent.summary
      || (agent.git_root ? agent.git_root.split('/').pop() : '')
      || (agent.cwd || '').split('/').pop()
      || '';
    if (label) {
      const trunc = label.length > 28 ? label.substring(0, 26) + '..' : label;
      ctx.font = '7px "Monaspace Krypton",monospace';
      ctx.fillStyle = stale ? 'rgba(106,110,149,0.3)' : 'rgba(221,247,255,0.5)';
      if (ax + trunc.length * 4 > canvasW - 10) {
        ctx.textAlign = 'right';
        ctx.fillText(trunc, ax - 6, ay + 2);
      } else {
        ctx.textAlign = 'left';
        ctx.fillText(trunc, ax + 6, ay + 2);
      }
      ctx.textAlign = 'center';
    }
  });
}

function drawTitle(ctx, w, byMachine) {
  ctx.font = '700 12px "Monaspace Krypton",monospace';
  ctx.textAlign = 'left';
  ctx.fillStyle = '#82FB9C';
  ctx.fillText('PEER NETWORK', 12, 20);

  const total = graphPeers.length;
  const activeMachines = Object.keys(byMachine).length;
  const onlineCount = Object.values(graphMachines).filter(m => m.status === 'online').length;

  ctx.font = '9px "Monaspace Krypton",monospace';
  ctx.fillStyle = '#6a6e95';
  ctx.fillText(`${total} AGENTS  /  ${activeMachines} ACTIVE  /  ${onlineCount} ONLINE`, 12, 36);

  ctx.textAlign = 'right';
  ctx.fillStyle = natsConnected ? 'rgba(80,247,212,0.6)' : 'rgba(255,107,107,0.6)';
  ctx.fillText(natsConnected ? 'NATS LIVE' : 'NATS OFF', w - 12, 20);
  ctx.textAlign = 'center';
}
