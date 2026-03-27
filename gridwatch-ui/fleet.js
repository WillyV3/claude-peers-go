// ========== PAGE 1: FLEET ==========

const MACHINES = ['omarchy', 'ubuntu-homelab', 'macbook1', 'thinkbook', 'raspdeck', 'willyv4', 'sonias-mbp'];

const DISPLAY_NAMES = {
  'omarchy': 'OMARCHY', 'ubuntu-homelab': 'U-HOMELAB', 'macbook1': 'MACBOOK',
  'sonias-mbp': 'SONIA-MBP', 'raspdeck': 'RASPDECK', 'willyv4': 'WILLYV4', 'thinkbook': 'THINKBOOK',
};

const LOGOS = {
  arch: `<svg viewBox="0 0 24 24"><path fill="#1793D1" d="M11.39.605C10.376 3.092 9.764 4.72 8.635 7.132c.693.734 1.543 1.589 2.923 2.554-1.484-.61-2.496-1.224-3.252-1.86C6.86 10.842 4.596 15.138 0 23.395c3.612-2.085 6.412-3.37 9.021-3.862a6.61 6.61 0 01-.171-1.547l.003-.115c.058-2.315 1.261-4.095 2.687-3.973 1.426.12 2.534 2.096 2.478 4.409a6.52 6.52 0 01-.146 1.243c2.58.505 5.352 1.787 8.914 3.844-.702-1.293-1.33-2.459-1.929-3.57-.943-.73-1.926-1.682-3.933-2.713 1.38.359 2.367.772 3.137 1.234-6.09-11.334-6.582-12.84-8.67-17.74z"/></svg>`,
  ubuntu: `<svg viewBox="0 0 24 24"><path fill="#E95420" d="M17.61.455a3.41 3.41 0 0 0-3.41 3.41 3.41 3.41 0 0 0 3.41 3.41 3.41 3.41 0 0 0 3.41-3.41 3.41 3.41 0 0 0-3.41-3.41zM12.92.8C8.923.777 5.137 2.941 3.148 6.451a4.5 4.5 0 0 1 .26-.007 4.92 4.92 0 0 1 2.585.737A8.316 8.316 0 0 1 12.688 3.6 4.944 4.944 0 0 1 13.723.834 11.008 11.008 0 0 0 12.92.8zm9.226 4.994a4.915 4.915 0 0 1-1.918 2.246 8.36 8.36 0 0 1-.273 8.303 4.89 4.89 0 0 1 1.632 2.54 11.156 11.156 0 0 0 .559-13.089zM3.41 7.932A3.41 3.41 0 0 0 0 11.342a3.41 3.41 0 0 0 3.41 3.409 3.41 3.41 0 0 0 3.41-3.41 3.41 3.41 0 0 0-3.41-3.41zm2.027 7.866a4.908 4.908 0 0 1-2.915.358 11.1 11.1 0 0 0 7.991 6.698 11.234 11.234 0 0 0 2.422.249 4.879 4.879 0 0 1-.999-2.85 8.484 8.484 0 0 1-.836-.136 8.304 8.304 0 0 1-5.663-4.32zm11.405.928a3.41 3.41 0 0 0-3.41 3.41 3.41 3.41 0 0 0 3.41 3.41 3.41 3.41 0 0 0 3.41-3.41 3.41 3.41 0 0 0-3.41-3.41z"/></svg>`,
  raspbian: `<svg viewBox="0 0 24 24"><path fill="#A22846" d="m19.8955 10.8961-.1726-.3028c.0068-2.1746-1.0022-3.061-2.1788-3.7348.356-.0938.7237-.1711.8245-.6182.6118-.1566.7397-.4398.8011-.7398.16-.1066.6955-.4061.6394-.9211.2998-.2069.4669-.4725.3819-.8487.3222-.3515.407-.6419.2702-.9096.3868-.4805.2152-.7295.05-.9817.2897-.5254.0341-1.0887-.7758-.9944-.3221-.4733-1.0244-.3659-1.133-.3637-.1215-.1519-.2819-.2821-.7755-.219-.3197-.2851-.6771-.2364-1.0458-.0964-.4378-.3403-.7275-.0675-1.0584.0356-.53-.1706-.6513.0631-.9117.1583-.5781-.1203-.7538.1416-1.0309.4182l-.3224-.0063c-.8719.5061-1.305 1.5366-1.4585 2.0664-.1536-.5299-.5858-1.5604-1.4575-2.0664l-.3223.0063C9.942.5014 9.7663.2394 9.1883.3597 8.9279.2646 8.807.0309 8.2766.2015c-.2172-.0677-.417-.2084-.6522-.2012l.0004.0002C7.5017.0041 7.369.049 7.2185.166c-.3688-.1401-.7262-.1887-1.0459.0964-.4936-.0631-.654.0671-.7756.219C5.2887.4791 4.5862.3717 4.264.845c-.8096-.0943-1.0655.4691-.7756.9944-.1653.2521-.3366.5013.05.9819-.1367.2677-.0519.5581.2703.9096-.085.3763.0822.6418.3819.8487-.0561.515.4795.8144.6394.9211.0614.3001.1894.5832.8011.7398.1008.4472.4685.5244.8245.6183-1.1766.6737-2.1856 1.56-2.1788 3.7348l-.1724.3028c-1.3491.8082-2.5629 3.4056-.6648 5.5167.124.6609.3319 1.1355.5171 1.6609.2769 2.117 2.0841 3.1082 2.5608 3.2255.6984.524 1.4423 1.0212 2.449 1.3696.949.964 1.977 1.3314 3.0107 1.3308.0152 0 .0306.0002.0457 0 1.0337.0006 2.0618-.3668 3.0107-1.3308 1.0067-.3483 1.7506-.8456 2.4491-1.3696.4766-.1173 2.2838-1.1085 2.5607-3.2255.1851-.5253.3931-1 .517-1.6609 1.8981-2.1113.6843-4.7089-.6649-5.517z"/></svg>`,
  macos: `<svg viewBox="0 0 24 24"><path fill="#999" d="M12.152 6.896c-.948 0-2.415-1.078-3.96-1.04-2.04.027-3.91 1.183-4.961 3.014-2.117 3.675-.546 9.103 1.519 12.09 1.013 1.454 2.208 3.09 3.792 3.039 1.52-.065 2.09-.987 3.935-.987 1.831 0 2.35.987 3.96.948 1.637-.026 2.676-1.48 3.676-2.948 1.156-1.688 1.636-3.325 1.662-3.415-.039-.013-3.182-1.221-3.22-4.857-.026-3.04 2.48-4.494 2.597-4.559-1.429-2.09-3.623-2.324-4.39-2.376-2-.156-3.675 1.09-4.61 1.09zM15.53 3.83c.843-1.012 1.4-2.427 1.245-3.83-1.207.052-2.662.805-3.532 1.818-.78.896-1.454 2.338-1.273 3.714 1.338.104 2.715-.688 3.559-1.701"/></svg>`,
};

const WIDE_TILES = ['sonias-mbp'];

function dedup(procs) {
  const m = {};
  for (const p of (procs || [])) {
    if (m[p.name]) m[p.name].mem_pct += p.mem_pct;
    else m[p.name] = { ...p };
  }
  return Object.values(m).sort((a, b) => b.mem_pct - a.mem_pct).slice(0, 4);
}

function makeTile(id) {
  const el = document.createElement('div');
  el.className = 'tile' + (WIDE_TILES.includes(id) ? ' wide' : '');
  el.id = `t-${id}`;
  el.innerHTML = `
    <div class="t-hdr">
      <span class="t-dot on" id="td-${id}"></span>
      <div class="t-logo" id="tl-${id}"></div>
      <span class="t-name">${DISPLAY_NAMES[id] || id}</span>
      <span class="t-ip" id="tip-${id}"></span>
    </div>
    <div class="t-gauges">
      <div class="tg"><div class="tg-top"><span class="tg-label">CPU</span><span class="tg-val" id="tv-${id}-cpu">--</span></div><div class="tg-bar"><div class="tg-fill" id="tb-${id}-cpu"></div></div></div>
      <div class="tg"><div class="tg-top"><span class="tg-label">RAM</span><span class="tg-val" id="tv-${id}-ram">--</span></div><div class="tg-bar"><div class="tg-fill" id="tb-${id}-ram"></div></div></div>
      <div class="tg"><div class="tg-top"><span class="tg-label">DSK</span><span class="tg-val" id="tv-${id}-dsk">--</span></div><div class="tg-bar"><div class="tg-fill" id="tb-${id}-dsk"></div></div></div>
    </div>
    <div class="t-procs" id="tp-${id}"></div>
    <div class="t-meta" id="tm-${id}"></div>
    <div class="t-agents-hdr" id="tah-${id}"></div>
    <div class="t-agents" id="ta-${id}"></div>
    <span class="t-offline">OFFLINE</span>`;
  return el;
}

function setBar(id, m, pct) {
  const bar = document.getElementById(`tb-${id}-${m}`);
  const val = document.getElementById(`tv-${id}-${m}`);
  if (!bar || !val) return;
  const p = pct || 0;
  const c = gCol(p);
  bar.style.width = p + '%';
  bar.style.backgroundColor = c;
  val.textContent = Math.round(p) + '%';
  val.style.color = c;
}

function updateTile(id, d) {
  const tile = document.getElementById(`t-${id}`);
  if (!tile) return;
  const dot = document.getElementById(`td-${id}`);
  const logo = document.getElementById(`tl-${id}`);
  const ip = document.getElementById(`tip-${id}`);
  const meta = document.getElementById(`tm-${id}`);

  if (d.os && LOGOS[d.os] && logo) logo.innerHTML = LOGOS[d.os];
  if (ip && d.ip) ip.textContent = d.ip;

  if (d.status !== 'online') {
    tile.classList.add('offline');
    tile.classList.remove('has-agents');
    if (dot) dot.className = `t-dot ${d.status === 'timeout' ? 'timeout' : 'off'}`;
    setBar(id, 'cpu', 0); setBar(id, 'ram', 0); setBar(id, 'dsk', 0);
    return;
  }

  tile.classList.remove('offline');
  if (dot) dot.className = 't-dot on';
  setBar(id, 'cpu', d.cpu);
  setBar(id, 'ram', d.mem_pct);
  setBar(id, 'dsk', d.disk_pct);

  // Disk threshold alerts.
  const dskBar = document.getElementById(`tb-${id}-dsk`);
  if (dskBar) {
    if (d.disk_pct >= 85) {
      dskBar.style.boxShadow = '0 0 6px rgba(255, 107, 107, 0.7)';
      tile.style.animation = 'diskAlert 2s ease-in-out infinite';
      tile.style.setProperty('--disk-alert-color', 'rgba(255, 107, 107, 0.15)');
    } else if (d.disk_pct >= 65) {
      dskBar.style.boxShadow = '0 0 6px rgba(164, 255, 236, 0.6)';
      tile.style.animation = '';
      tile.style.removeProperty('--disk-alert-color');
    } else {
      dskBar.style.boxShadow = '';
      tile.style.animation = '';
      tile.style.removeProperty('--disk-alert-color');
    }
  }

  if (meta) {
    let m = d.specs || '';
    if (d.uptime) {
      let u = d.uptime.replace('up ', '');
      if (u.length > 16) u = u.replace(/ minutes?/g, 'm').replace(/ hours?/g, 'h').replace(/ days?/g, 'd').replace(/ weeks?/g, 'w').replace(/, /g, ' ');
      m += m ? ' | ' + u : u;
    }
    meta.textContent = m;
  }

  const procs = document.getElementById(`tp-${id}`);
  if (procs) {
    procs.innerHTML = dedup(d.processes).map(p =>
      `<span class="t-proc">${esc(p.name)}<span class="t-proc-pct">${p.mem_pct.toFixed(1)}%</span></span>`
    ).join('');
  }
}

function updateAgentsInTiles(peers) {
  cachedPeers = peers;
  const grouped = {};
  for (const p of peers) {
    const mid = normalizeMachine(p.machine);
    if (!grouped[mid]) grouped[mid] = [];
    grouped[mid].push(p);
  }

  for (const id of MACHINES) {
    const tile = document.getElementById(`t-${id}`);
    const agentsEl = document.getElementById(`ta-${id}`);
    if (!agentsEl) continue;
    const agents = grouped[id] || [];
    const hdrEl = document.getElementById(`tah-${id}`);

    if (agents.length > 0) {
      tile.classList.add('has-agents');
      if (hdrEl) {
        const active = agents.filter(a => !a.last_seen || (Date.now() - new Date(a.last_seen).getTime()) <= 60000).length;
        hdrEl.innerHTML = `<span class="t-agents-label">CLAUDE</span><span class="t-agents-count">${active} live</span><span class="t-agents-line"></span>`;
      }
      agentsEl.innerHTML = agents.map(a => {
        const repo = a.git_root ? a.git_root.split('/').pop() : '';
        const cwd = (a.cwd || '').replace(/^\/home\/\w+\//, '~/').replace(/^\/Users\/\w+\//, '~/');
        const label = a.summary || repo || cwd.split('/').pop() || 'session';
        const stale = a.last_seen && (Date.now() - new Date(a.last_seen).getTime()) > 60000;
        const tty = a.tty ? a.tty.replace('pts/', 'pty/') : '';
        const upSec = a.registered_at ? Math.floor((Date.now() - new Date(a.registered_at).getTime()) / 1000) : 0;
        const upStr = upSec > 3600 ? Math.floor(upSec / 3600) + 'h' : upSec > 60 ? Math.floor(upSec / 60) + 'm' : upSec + 's';
        const meta = [tty, repo, upStr].filter(Boolean).join(' \u00b7 ');
        return `<div class="ta ${stale ? 'inactive' : ''}"><span class="ta-dot"></span><div class="ta-body"><span class="ta-text">${esc(label)}</span>${meta ? `<span class="ta-meta">${esc(meta)}</span>` : ''}</div></div>`;
      }).join('');
    } else {
      tile.classList.remove('has-agents');
      if (hdrEl) hdrEl.innerHTML = '';
      agentsEl.innerHTML = '';
    }
  }
}

function updateLLM(data) {
  const tile = document.getElementById('t-sonias-mbp');
  if (!tile) return;
  let el = document.getElementById('llm-status');
  if (!el) {
    el = document.createElement('div');
    el.id = 'llm-status';
    el.className = 'llm-section';
    const agents = document.getElementById('ta-sonias-mbp');
    if (agents) agents.before(el); else tile.appendChild(el);
  }

  if (data.status !== 'online') {
    el.innerHTML = `<div class="llm-header"><span class="llm-dot" style="background:var(--red)"></span><span class="llm-model">LLM OFFLINE</span></div>`;
    tile.style.borderColor = '';
    return;
  }

  const model = (data.model || '').split('/').pop() || '?';
  const slots = data.slots || [];
  const total = data.total_slots || slots.length;
  const active = slots.filter(s => s.processing).length;
  const isActive = active > 0;
  const m = data.metrics || {};
  const slotBars = slots.map(s => {
    const dec = s.decoded || 0, rem = s.remaining || 0, tot = dec + rem;
    const pct = tot > 0 ? (dec / tot * 100) : 0;
    return `<div class="llm-slot"><div class="llm-slot-fill ${s.processing ? 'active' : 'idle'}" style="width:${s.processing ? pct : 0}%"></div></div>`;
  }).join('');
  const promptTps = (m.prompt_tokens_seconds || 0).toFixed(1);
  const genTps = (m.predicted_tokens_seconds || 0).toFixed(1);
  const reqActive = Math.round(m.requests_processing || 0);
  const badge = isActive ? '<span class="llm-status-badge active">INFERRING</span>'
    : reqActive > 0 ? '<span class="llm-status-badge active">PROCESSING</span>'
    : '<span class="llm-status-badge idle">IDLE</span>';

  el.innerHTML = `
    <div class="llm-header"><span class="llm-dot" style="background:${isActive || reqActive > 0 ? 'var(--cyan)' : 'var(--dim)'}"></span><span class="llm-model">${esc(model)}</span>${badge}</div>
    <div style="display:flex;gap:10px">
      <div style="flex:1">
        <div class="llm-row"><span class="lbl">SLOTS</span><span class="${isActive ? 'val-hl' : 'val'}">${active}/${total}</span></div>
        <div class="llm-slots">${slotBars}</div>
        <div class="llm-row"><span class="lbl">REQS</span><span class="${reqActive > 0 ? 'val-hl' : 'val'}">${reqActive} active</span></div>
      </div>
      <div style="flex:1">
        <div class="llm-row"><span class="lbl">PROMPT</span><span class="val">${fmtK(Math.round(m.prompt_tokens_total || 0))} tok</span></div>
        <div class="llm-row"><span class="lbl">GEN</span><span class="val">${fmtK(Math.round(m.tokens_predicted_total || 0))} tok</span></div>
        <div class="llm-row"><span class="lbl">DECODES</span><span class="val">${fmtK(Math.round(m.n_decode_total || 0))}</span></div>
      </div>
      <div style="flex:1">
        <div class="llm-row"><span class="lbl">IN</span><span class="${parseFloat(promptTps) > 0 ? 'val-hl' : 'val'}">${promptTps} t/s</span></div>
        <div class="llm-row"><span class="lbl">OUT</span><span class="${parseFloat(genTps) > 0 ? 'val-hl' : 'val'}">${genTps} t/s</span></div>
        <div class="llm-row"><span class="lbl">PORT</span><span class="val">:8080</span></div>
      </div>
    </div>`;
  tile.style.borderColor = isActive ? 'rgba(80, 247, 212, 0.3)' : '';
}

function updateWillyv4Tile(d) {
  const tile = document.getElementById('t-willyv4');
  if (!tile) return;
  let el = document.getElementById('v4-device');
  if (!el) {
    el = document.createElement('div');
    el.id = 'v4-device';
    el.className = 'llm-section';
    const agents = document.getElementById('ta-willyv4');
    if (agents) agents.before(el); else tile.appendChild(el);
  }
  const batt = d.battery || 0, power = d.power || 'unknown';
  const attention = d.attention || 'unknown', network = d.network || '?';
  const tailscale = d.tailscale ? 'connected' : 'down';
  const powerColor = power === 'active' ? 'var(--green)' : power === 'idle' ? 'var(--warn)' : 'var(--dim)';
  const battColor = batt < 20 ? 'var(--red)' : batt < 50 ? 'var(--warn)' : 'var(--green)';

  el.innerHTML = `
    <div class="llm-header"><span class="llm-dot" style="background:${powerColor}"></span><span class="llm-model">CYBERDECK</span><span class="llm-status-badge ${power === 'active' ? 'active' : 'idle'}">${power.toUpperCase()}</span></div>
    <div style="display:flex;gap:10px">
      <div style="flex:1"><div class="llm-row"><span class="lbl">BATT</span><span style="color:${battColor}" class="val">${batt}%</span></div><div class="llm-row"><span class="lbl">ATTN</span><span class="val">${attention}</span></div></div>
      <div style="flex:1"><div class="llm-row"><span class="lbl">NET</span><span class="val">${network}</span></div><div class="llm-row"><span class="lbl">TS</span><span class="val">${tailscale}</span></div></div>
    </div>`;
  tile.style.borderColor = power === 'active' ? 'rgba(80, 248, 114, 0.2)' : '';
}
