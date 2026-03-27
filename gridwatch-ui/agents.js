// ========== PAGE 4: DAEMON AGENTS ==========
// Each daemon is a mini terminal: header bar + live output.

const DAEMON_DEFS = {
  'fleet-memory': { tag: 'FM', color: '#7cf8f7', schedule: 'event:fleet.>' },
  'fleet-scout':  { tag: 'FS', color: '#50f872', schedule: 'interval:15m' },
  'llm-watchdog': { tag: 'LW', color: '#D97757', schedule: 'interval:15m' },
  'pr-helper':    { tag: 'PR', color: '#829dd4', schedule: 'interval:30m' },
  'sync-janitor': { tag: 'SJ', color: '#a4ffec', schedule: 'interval:30m' },
};

function daemonDef(name) {
  if (DAEMON_DEFS[name]) return DAEMON_DEFS[name];
  const tag = name.split('-').map(w => (w[0] || '')).join('').toUpperCase().slice(0, 2) || '??';
  const hue = [...name].reduce((h, c) => h + c.charCodeAt(0), 0) % 360;
  return { tag, color: `hsl(${hue}, 70%, 65%)`, schedule: '?' };
}

// Clean raw daemon output into readable text.
function cleanDaemonOutput(raw) {
  if (!raw) return '';
  let s = raw;

  // Extract content from JSON envelope if present (agent binary wraps output).
  const jsonMatch = s.match(/"(?:check_\w+|summarize|research|main)":\s*"([\s\S]+?)"\s*[,}]/);
  if (jsonMatch) s = jsonMatch[1];

  // Unescape JSON string escapes.
  s = s.replace(/\\n/g, ' ').replace(/\\"/g, '"').replace(/\\\\/g, '\\');

  // Strip markdown bold/italic.
  s = s.replace(/\*\*([^*]+)\*\*/g, '$1').replace(/\*([^*]+)\*/g, '$1');

  // Strip markdown headers.
  s = s.replace(/^#+\s*/gm, '');

  // Strip markdown bullets to plain text.
  s = s.replace(/^[\s]*[-*]\s+/gm, '- ');

  // Collapse whitespace.
  s = s.replace(/\s+/g, ' ').trim();

  return s;
}

const daemonHistory = {};

function updateDaemonsPage(natsData) {
  const el = document.getElementById('dmn-layout');
  if (!el) return;

  const runs = (natsData.daemon_runs || []).concat(natsData.events || []).filter(e =>
    e.type && e.type.startsWith('daemon_')
  );

  const byDaemon = {};
  for (const r of runs) {
    const name = r.peer_id || '?';
    if (!byDaemon[name]) byDaemon[name] = [];
    byDaemon[name].push(r);
  }
  for (const [name, newRuns] of Object.entries(byDaemon)) {
    if (!daemonHistory[name]) daemonHistory[name] = [];
    const seen = new Set(daemonHistory[name].map(r => r.timestamp));
    for (const r of newRuns) {
      if (r.timestamp && !seen.has(r.timestamp)) {
        daemonHistory[name].push(r);
        seen.add(r.timestamp);
      }
    }
    daemonHistory[name] = daemonHistory[name].slice(-20);
  }

  const allNames = [...new Set([...Object.keys(DAEMON_DEFS), ...Object.keys(daemonHistory)])];
  const lineClamp = allNames.length <= 3 ? 4 : allNames.length <= 5 ? 3 : 2;

  let html = '';
  for (const name of allNames) {
    const def = daemonDef(name);
    const history = daemonHistory[name] || [];
    const latest = history[history.length - 1];
    const status = latest
      ? (latest.type === 'daemon_complete' ? 'complete' : latest.type === 'daemon_failed' ? 'failed' : 'idle')
      : 'idle';

    const dataStr = latest?.data || '';
    const trigger = dataStr.match(/trigger=(\S+)/)?.[1] || '-';
    const duration = dataStr.match(/duration=(\S+)/)?.[1] || '-';
    const lastRun = latest?.timestamp ? timeAgo(latest.timestamp) : '-';
    const ok = history.filter(r => r.type === 'daemon_complete').length;
    const fail = history.filter(r => r.type === 'daemon_failed').length;

    const oneHourAgo = Date.now() - 3600000;
    const runsPerHour = history.filter(r => r.timestamp && new Date(r.timestamp).getTime() > oneHourAgo).length;
    const schedParts = def.schedule.split(':');
    const isEvent = schedParts[0] === 'event';
    const schedLabel = isEvent ? 'EVT' : (schedParts[1] || '?').toUpperCase();
    const maxRate = isEvent ? 10 : Math.ceil(60 / parseInt(schedParts[1] || '15')) + 2;
    const rateHigh = runsPerHour > maxRate;

    // Sparkline (last 8).
    const barRuns = history.slice(-8);
    let spark = '';
    for (let i = 0; i < 8; i++) {
      if (i < barRuns.length) {
        const r = barRuns[i];
        const d = parseInt(r.data?.match(/duration=(\d+)/)?.[1] || '30');
        const h = Math.max(2, Math.min(10, (d / 300) * 10));
        spark += `<div class="dmn-spark-bar ${r.type === 'daemon_complete' ? 'ok' : 'err'}" style="height:${h}px"></div>`;
      } else {
        spark += '<div class="dmn-spark-bar empty"></div>';
      }
    }

    // The output -- the hero. Clean it up from raw daemon output.
    const output = cleanDaemonOutput(latest?.summary || '');

    html += `<div class="dmn-entry ${status}">
      <div class="dmn-bar">
        <div class="dmn-accent" style="background:${def.color}"></div>
        <span class="dmn-tag" style="background:${def.color}15;color:${def.color};border-color:${def.color}35">${def.tag}</span>
        <span class="dmn-dot ${status}"></span>
        <span class="dmn-name">${esc(name)}</span>
        <span class="dmn-sched">${schedLabel}</span>
        <div class="dmn-stats">
          <span class="ds"><span class="val">${lastRun}</span></span>
          <span class="ds"><span class="val-hl">${duration}</span></span>
          <span class="ds"><span class="val">${trigger}</span></span>
          <span class="ds"><span class="${rateHigh ? 'val-err' : 'val'}">${runsPerHour}/h</span></span>
          <span class="ds"><span class="val-ok">${ok}</span>${fail > 0 ? `/<span class="val-err">${fail}</span>` : ''}</span>
          <div class="dmn-spark">${spark}</div>
        </div>
      </div>
      ${output
        ? `<div class="dmn-output"><div class="dmn-output-text" style="-webkit-line-clamp:${lineClamp}">${esc(output)}</div></div>`
        : '<div class="dmn-waiting">awaiting first run</div>'}
    </div>`;
  }

  el.innerHTML = html;
}
