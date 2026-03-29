// ========== GRIDWATCH CORE ==========
// Carousel, clock, polling, shared utilities.

const PAGE_LABELS = ['FLEET', 'SERVICES', 'NATS', 'AGENTS', 'PEERS', 'SECURITY'];
const ROTATE_MS = 15000;

// --- Utilities ---

function gCol(p) { return p >= 85 ? 'var(--red)' : p >= 65 ? 'var(--warn)' : 'var(--mint)'; }

function esc(s) {
  const d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

function fB(b) {
  if (!b) return '0B';
  const u = ['B','KB','MB','GB','TB'];
  const i = Math.floor(Math.log(b) / Math.log(1024));
  return (b / Math.pow(1024, i)).toFixed(1) + u[i];
}

function fmtK(n) {
  return n >= 1e6 ? (n / 1e6).toFixed(1) + 'M' : n >= 1e3 ? (n / 1e3).toFixed(1) + 'K' : String(n);
}

function timeAgo(iso) {
  if (!iso) return '?';
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 0) return '0s';
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s / 60) + 'm';
  if (s < 86400) return Math.floor(s / 3600) + 'h';
  return Math.floor(s / 86400) + 'd';
}

function normalizeMachine(name) {
  if (!name) return '';
  const n = name.toLowerCase().replace('.local', '');
  if (n.includes('ubuntu') || n.includes('homelab')) return 'ubuntu-homelab';
  if (n.includes('sonia')) return 'sonias-mbp';
  if (n.includes('thinkbook')) return 'thinkbook';
  return n;
}

// --- Carousel ---

let currentPage = 0;
let rotateTimer = null;
let autoRotate = true;
let pauseTimeout = null;

function goToPage(idx) {
  if (idx === currentPage) return;
  const pages = document.querySelectorAll('.page');
  const dots = document.querySelectorAll('.dot');
  const prev = currentPage;

  currentPage = idx;
  pages[prev].classList.remove('active');
  pages[prev].classList.add('exit-left');

  requestAnimationFrame(() => {
    pages[idx].classList.add('active');
    dots.forEach((d, i) => d.classList.toggle('active', i === idx));
    document.getElementById('page-label').textContent = PAGE_LABELS[idx];
  });

  setTimeout(() => pages[prev].classList.remove('exit-left'), 450);

  // Start/stop peer graph canvas animation based on page visibility.
  if (idx === PAGE_LABELS.indexOf('PEERS')) startPeerGraph();
  else stopPeerGraph();

  if (autoRotate) resetRotateTimer();
  updatePauseIndicator();
}

function prevPage() { goToPage((currentPage - 1 + PAGE_LABELS.length) % PAGE_LABELS.length); }
function nextPage() { goToPage((currentPage + 1) % PAGE_LABELS.length); }

function pauseRotation(durationMs) {
  autoRotate = false;
  if (rotateTimer) { clearInterval(rotateTimer); rotateTimer = null; }
  if (pauseTimeout) clearTimeout(pauseTimeout);
  if (durationMs) {
    pauseTimeout = setTimeout(() => { resumeRotation(); }, durationMs);
  }
  updatePauseIndicator();
}

function resumeRotation() {
  autoRotate = true;
  if (pauseTimeout) { clearTimeout(pauseTimeout); pauseTimeout = null; }
  resetRotateTimer();
  updatePauseIndicator();
}

function togglePause() {
  if (autoRotate) pauseRotation(0); // indefinite pause
  else resumeRotation();
}

function resetRotateTimer() {
  if (rotateTimer) clearInterval(rotateTimer);
  if (autoRotate) rotateTimer = setInterval(nextPage, ROTATE_MS);
}

function updatePauseIndicator() {
  const dots = document.getElementById('page-dots');
  if (dots) dots.classList.toggle('paused', !autoRotate);
  const label = document.getElementById('page-label');
  if (label) {
    label.classList.toggle('paused', !autoRotate);
  }
}

// --- Clock (rAF-driven) ---

function tickClock() {
  const now = new Date();
  const el = document.getElementById('clock');
  if (el) {
    el.textContent = [now.getHours(), now.getMinutes(), now.getSeconds()]
      .map(v => String(v).padStart(2, '0')).join(':');
  }
  setTimeout(tickClock, 1000 - now.getMilliseconds());
}

// --- Footer ---

let cachedPeers = [];
let natsConnected = false;

function updateFtr(machines) {
  const v = Object.values(machines);
  const on = v.filter(m => m.status === 'online');
  const avgCpu = on.length ? (on.reduce((s, m) => s + (m.cpu || 0), 0) / on.length).toFixed(1) : '0';
  const totMem = on.reduce((s, m) => s + (m.mem_total || 0), 0);
  const useMem = on.reduce((s, m) => s + (m.mem_used || 0), 0);

  document.getElementById('summary').innerHTML =
    `<span class="on-n">${on.length} ON</span>${v.length - on.length ? ` <span class="off-n">${v.length - on.length} OFF</span>` : ''}`;

  document.getElementById('ftr-stats').innerHTML =
    `<span>CPU <span class="v">${avgCpu}%</span></span><span class="sep">|</span>` +
    `<span>MEM <span class="v">${fB(useMem)}</span>/<span class="v">${fB(totMem)}</span></span><span class="sep">|</span>` +
    `<span><span class="v">${cachedPeers.length}</span> AGENTS</span><span class="sep">|</span>` +
    `<span>${natsConnected ? '<span class="v">NATS</span>' : '<span style="color:var(--red)">NATS OFF</span>'}</span>`;
}

const LEVEL_COLORS = { error: 'var(--red)', warn: 'var(--warn)', info: 'var(--mint)' };
const TYPE_LABELS = {
  svc: 'SVC', docker: 'DOCKER', sync: 'SYNC', daemon: 'DAEMON',
  peer: 'PEER', disk: 'DISK', chezmoi: 'CHEZMOI', nats: 'NATS',
  security: 'SEC', quarantine: 'QRTN'
};

let lastTickerHash = '';
let tickerOffset = 0;
let tickerWidth = 0;
let tickerRaf = null;
let lastTickerFrame = 0;
const TICKER_SPEED = 40; // px/sec

function tickerLoop(ts) {
  if (!lastTickerFrame) lastTickerFrame = ts;
  const dt = (ts - lastTickerFrame) / 1000;
  lastTickerFrame = ts;

  tickerOffset -= TICKER_SPEED * dt;
  if (tickerWidth > 0 && tickerOffset <= -tickerWidth) {
    tickerOffset += tickerWidth;
  }

  const inner = document.querySelector('.ticker-inner');
  if (inner) {
    inner.style.transform = `translateX(${tickerOffset}px)`;
  }
  tickerRaf = requestAnimationFrame(tickerLoop);
}

function renderTicker(events) {
  const el = document.getElementById('ticker');
  if (!el) return;

  if (!events || !events.length) {
    el.innerHTML = '<div class="ticker-inner"><span class="tk"><span class="tk-type" style="color:var(--dim)">IDLE</span> <span class="tk-data">no recent events</span></span></div>';
    return;
  }

  const hash = events.map(e => e.time + e.type + e.title).join('');
  if (hash === lastTickerHash) return;
  lastTickerHash = hash;

  const items = events.slice(0, 25).map(e => {
    const ago = timeAgo(e.time);
    const color = LEVEL_COLORS[e.level] || 'var(--dim)';
    const label = TYPE_LABELS[e.type] || e.type.toUpperCase();
    return `<span class="tk"><span class="tk-ago">${ago}</span> <span class="tk-type" style="color:${color}">${label}</span> <span class="tk-data">${esc(e.title)}</span>${e.detail ? ` <span class="tk-detail">${esc(e.detail)}</span>` : ''}</span><span class="tk-sep">\u00b7</span>`;
  }).join('');

  // Build content: duplicate for seamless wrap.
  el.innerHTML = `<div class="ticker-inner">${items}${items}</div>`;

  // Measure half-width for wrap point.
  const inner = el.querySelector('.ticker-inner');
  if (inner) {
    inner.style.animation = 'none';
    tickerWidth = inner.scrollWidth / 2;
    // Clamp offset to new width so it doesn't jump.
    if (tickerWidth > 0) {
      tickerOffset = tickerOffset % tickerWidth;
      if (tickerOffset > 0) tickerOffset -= tickerWidth;
    }
  }

  // Start rAF loop if not running.
  if (!tickerRaf) {
    lastTickerFrame = 0;
    tickerRaf = requestAnimationFrame(tickerLoop);
  }
}

// --- Polling ---

async function poll(url) {
  const resp = await fetch(url);
  return resp.json();
}

async function pollAll() {
  // Parallel fetches.
  const [stats, peers, llm, nats, willyv4, services, natsStats, ticker, security] = await Promise.allSettled([
    poll('/api/stats'),
    poll('/api/peers'),
    poll('/api/llm'),
    poll('/api/nats'),
    poll('/api/willyv4'),
    poll('/api/services'),
    poll('/api/nats-stats'),
    poll('/api/ticker'),
    poll('/api/security'),
  ]);

  // Page 1: Fleet.
  if (stats.status === 'fulfilled' && stats.value.machines) {
    for (const [id, m] of Object.entries(stats.value.machines)) updateTile(id, m);
    updateFtr(stats.value.machines);
  }
  if (peers.status === 'fulfilled') {
    updateAgentsInTiles(peers.value.peers || []);
  }
  if (llm.status === 'fulfilled') updateLLM(llm.value);
  if (willyv4.status === 'fulfilled' && willyv4.value && willyv4.value.battery) {
    updateWillyv4Tile(willyv4.value);
  }
  if (security.status === 'fulfilled') {
    updateSecurityBadges(security.value);
    updateSecurityPage(security.value);
  }

  // Page 5: Peers (needs both stats + peers data).
  if (stats.status === 'fulfilled' || peers.status === 'fulfilled') {
    updatePeersPage(
      stats.status === 'fulfilled' ? stats.value.machines : null,
      peers.status === 'fulfilled' ? peers.value.peers : null
    );
  }

  // Page 2: Services.
  if (services.status === 'fulfilled') updateServicesPage(services.value);

  // Page 3: NATS dashboard.
  if (natsStats.status === 'fulfilled') updateNATSPage(natsStats.value);

  // Page 4: Agents (from NATS events data).
  if (nats.status === 'fulfilled') {
    const nd = nats.value;
    natsConnected = nd.connected;
    updateDaemonsPage(nd);
  }

  // Ticker (unified event bus).
  if (ticker.status === 'fulfilled') renderTicker(ticker.value);
}

// --- Init ---

function init() {
  // Build page 1.
  const grid = document.getElementById('grid');
  for (const id of MACHINES) grid.appendChild(makeTile(id));

  // Dot click/tap: go to page + pause for 2 minutes.
  document.querySelectorAll('.dot').forEach(dot => {
    dot.addEventListener('click', e => {
      e.stopPropagation();
      goToPage(parseInt(dot.dataset.page));
      pauseRotation(120000); // 2 min pause
    });
  });

  // Tap carousel body: toggle pause/resume.
  const carousel = document.getElementById('carousel');
  carousel.addEventListener('click', e => {
    // Only toggle if tap target is the carousel itself or a page, not a dot
    if (e.target.closest('.dot')) return;
    if (autoRotate) {
      pauseRotation(120000); // 2 min pause
    } else {
      resumeRotation();
    }
  });

  // Swipe left/right on touch devices.
  let touchStartX = 0;
  let touchStartY = 0;
  carousel.addEventListener('touchstart', e => {
    touchStartX = e.touches[0].clientX;
    touchStartY = e.touches[0].clientY;
  }, { passive: true });

  carousel.addEventListener('touchend', e => {
    const dx = e.changedTouches[0].clientX - touchStartX;
    const dy = e.changedTouches[0].clientY - touchStartY;
    // Only register horizontal swipes (|dx| > 50, |dx| > |dy|)
    if (Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy)) {
      if (dx < 0) nextPage(); else prevPage();
      pauseRotation(120000);
    }
  }, { passive: true });

  // Keyboard: left/right arrows navigate, space toggles pause.
  document.addEventListener('keydown', e => {
    if (e.key === 'ArrowRight') { nextPage(); pauseRotation(120000); }
    if (e.key === 'ArrowLeft') { prevPage(); pauseRotation(120000); }
    if (e.key === ' ') { e.preventDefault(); togglePause(); }
  });

  // Start clock.
  tickClock();

  // Initial poll + interval.
  pollAll();
  setInterval(pollAll, 3000);

  // Auto-rotate.
  resetRotateTimer();
}

document.addEventListener('DOMContentLoaded', init);
