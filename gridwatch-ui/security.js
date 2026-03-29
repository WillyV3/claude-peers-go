// ========== PAGE 6: SECURITY OPERATIONS ==========
// Control room view. Perimeter status + machine monitoring + live event feed.

let lastSecHTML = '';

function updateSecurityPage(healthMap) {
  const el = document.getElementById('sec-layout');
  if (!el) return;

  const hasData = healthMap && Object.keys(healthMap).length > 0;

  // Build machine list from stats + health data
  const allIds = new Set();
  if (typeof MACHINES !== 'undefined') for (const m of MACHINES) allIds.add(m);
  if (hasData) for (const k of Object.keys(healthMap)) allIds.add(normalizeMachine(k) || k);
  const machineIds = Array.from(allIds).sort();

  // Collect per-machine state
  const rows = [];
  let totalAgents = 0;
  let totalDegraded = 0;
  let totalQuarantined = 0;
  const allEvents = [];

  for (const id of machineIds) {
    const h = hasData ? (healthMap[id] || healthMap[normalizeMachine(id)]) : null;
    const score = h ? h.score : 0;
    const status = h ? h.status : 'healthy';
    const lastDesc = h ? h.last_event_desc : '';
    const lastTime = h ? h.last_event : '';
    const events = h ? (h.events || []) : [];
    const demotedAt = h ? h.demoted_at : '';
    const hasAgent = !!h; // has produced at least one event

    if (status === 'degraded') totalDegraded++;
    if (status === 'quarantined') totalQuarantined++;
    if (hasAgent) totalAgents++;

    // Determine monitoring type
    let monType = 'NO AGENT';
    if (id === 'willyv4') monType = 'SENTINEL';
    else if (hasAgent) monType = 'MONITORED';
    else monType = 'AWAITING';

    const ago = lastTime ? secTimeAgo(lastTime) : '';

    rows.push({ id, score, status, lastDesc, ago, events, demotedAt, monType, hasAgent });

    // Collect events for the live feed
    for (const e of events) {
      allEvents.push({ machine: id, text: e, time: lastTime });
    }
  }

  // Perimeter status
  const perimeterOk = totalQuarantined === 0 && totalDegraded === 0;
  const perimeterClass = totalQuarantined > 0 ? 'breach' : totalDegraded > 0 ? 'alert' : 'secure';
  const perimeterText = totalQuarantined > 0 ? 'BREACH DETECTED'
    : totalDegraded > 0 ? 'ELEVATED THREAT'
    : 'PERIMETER SECURE';

  let html = '';

  // Header
  html += `<div class="soc-header">
    <span class="soc-title">FLEET SECURITY</span>
    <span class="soc-agents">${totalAgents} AGENTS REPORTING</span>
    <span class="soc-nats ${natsConnected ? 'live' : 'off'}">${natsConnected ? 'NATS LIVE' : 'NATS OFF'}</span>
  </div>`;

  // Perimeter banner
  html += `<div class="soc-perimeter ${perimeterClass}">
    <span class="soc-perimeter-text">${perimeterText}</span>
    ${totalDegraded > 0 ? `<span class="soc-perimeter-detail">${totalDegraded} degraded</span>` : ''}
    ${totalQuarantined > 0 ? `<span class="soc-perimeter-detail">${totalQuarantined} quarantined</span>` : ''}
  </div>`;

  // Machine rows
  html += '<div class="soc-machines">';
  for (const r of rows) {
    const name = secName(r.id);
    const dotClass = r.status === 'quarantined' ? 'qrtn'
      : r.status === 'degraded' ? 'degraded'
      : r.hasAgent ? 'ok' : 'waiting';
    const scoreColor = r.score >= 10 ? 'var(--red)' : r.score >= 5 ? 'var(--warn)' : r.score > 0 ? 'var(--teal)' : 'var(--dim)';

    html += `<div class="soc-row ${r.status}">`;
    html += `<span class="soc-dot ${dotClass}"></span>`;
    html += `<span class="soc-machine-name">${name}</span>`;
    html += `<span class="soc-mon-type ${r.monType.toLowerCase()}">${r.monType}</span>`;
    html += `<span class="soc-score" style="color:${scoreColor}">score ${r.score}</span>`;

    if (r.status === 'quarantined' && r.demotedAt) {
      const dur = secTimeAgo(r.demotedAt);
      html += `<span class="soc-last qrtn">QUARANTINED ${dur}</span>`;
    } else if (r.lastDesc) {
      html += `<span class="soc-last">last: ${esc(r.lastDesc).substring(0, 45)} (${r.ago})</span>`;
    } else if (r.monType === 'AWAITING') {
      html += `<span class="soc-last waiting">(awaiting first event)</span>`;
    } else if (r.monType === 'SENTINEL') {
      html += `<span class="soc-last waiting">(sentinel active)</span>`;
    } else {
      html += `<span class="soc-last waiting">(no recent events)</span>`;
    }

    html += `</div>`;
  }
  html += '</div>';

  // Live feed -- recent events from all machines
  const feedEvents = [];
  if (hasData) {
    for (const [machine, data] of Object.entries(healthMap)) {
      const mid = normalizeMachine(machine) || machine;
      for (const e of (data.events || [])) {
        feedEvents.push({ machine: mid, text: e, time: data.last_event });
      }
    }
  }

  html += '<div class="soc-feed">';
  html += '<div class="soc-feed-hdr">LIVE FEED</div>';
  if (feedEvents.length === 0) {
    html += '<div class="soc-feed-empty">Monitoring active -- no events to display</div>';
  } else {
    html += '<div class="soc-feed-list">';
    // Show most recent events (deduplicated by text, max 12)
    const seen = new Set();
    let shown = 0;
    for (const fe of feedEvents) {
      if (shown >= 12) break;
      const key = fe.machine + fe.text;
      if (seen.has(key)) continue;
      seen.add(key);

      const { severity, text } = parseSev(fe.text);
      const sevClass = severity === 'critical' ? 'crit' : severity === 'warn' ? 'warn' : 'info';
      const name = secName(fe.machine);

      html += `<div class="soc-feed-row">`;
      html += `<span class="soc-feed-sev ${sevClass}">${severity.toUpperCase()}</span>`;
      html += `<span class="soc-feed-machine">${name}</span>`;
      html += `<span class="soc-feed-text">${esc(text)}</span>`;
      html += `</div>`;
      shown++;
    }
    html += '</div>';
  }
  html += '</div>';

  if (html !== lastSecHTML) {
    el.innerHTML = html;
    lastSecHTML = html;
  }
}

function secName(id) {
  const map = {
    'omarchy': 'OMARCHY', 'ubuntu-homelab': 'U-HOMELAB', 'raspdeck': 'RASPDECK',
    'thinkbook': 'THINKBOOK', 'willyv4': 'WILLYV4', 'macbook1': 'MACBOOK',
    'sonias-mbp': 'SONIA-MBP',
  };
  return map[id] || id.toUpperCase();
}

function parseSev(str) {
  if (!str) return { severity: 'info', text: '' };
  const l = str.toLowerCase();
  if (l.startsWith('critical:')) return { severity: 'critical', text: str.substring(9).trim() };
  if (l.startsWith('warn:')) return { severity: 'warn', text: str.substring(5).trim() };
  if (l.startsWith('warning:')) return { severity: 'warn', text: str.substring(8).trim() };
  if (l.startsWith('info:')) return { severity: 'info', text: str.substring(5).trim() };
  return { severity: 'info', text: str };
}

function secTimeAgo(iso) {
  if (!iso) return '';
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 0) return '0s';
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s / 60) + 'm';
  if (s < 86400) return Math.floor(s / 3600) + 'h';
  return Math.floor(s / 86400) + 'd';
}
