// ========== PAGE 2: SERVICES ==========

function updateServicesPage(data) {
  const el = document.getElementById('svc-layout');
  if (!el || !data || !data.timestamp) return;

  // Column 1: Infrastructure + Docker
  let col1 = '<div class="svc-col">';

  // Failed systemd units -- show at top if any exist.
  const failedUnits = data.failed_units || [];
  if (failedUnits.length > 0) {
    col1 += `<div class="svc-col-hdr" style="color:var(--red)">FAILED UNITS</div>`;
    for (const u of failedUnits) {
      col1 += `<div class="svc-row down">
        <span class="svc-dot down"></span>
        <span class="svc-name">${esc(u.name || u)}</span>
        <span class="svc-badge unhealthy">FAILED</span>
      </div>`;
    }
  }

  col1 += '<div class="svc-col-hdr">INFRASTRUCTURE</div>';
  for (const svc of (data.services || [])) {
    col1 += `<div class="svc-row ${svc.status}">
      <span class="svc-dot ${svc.status}"></span>
      <span class="svc-name">${esc(svc.name)}</span>
      ${svc.port ? `<span class="svc-port">:${svc.port}</span>` : ''}
      <span class="svc-latency">${svc.status === 'up' ? svc.latency_ms + 'ms' : svc.detail || 'DOWN'}</span>
    </div>`;
  }
  for (const c of (data.docker || [])) {
    const dotClass = c.health === 'unhealthy' ? 'unhealthy' : c.status === 'running' ? 'running' : 'down';
    const badge = c.health === 'healthy' ? '<span class="svc-badge healthy">OK</span>'
      : c.health === 'unhealthy' ? '<span class="svc-badge unhealthy">BAD</span>'
      : '<span class="svc-badge none">\u2014</span>';
    const restartBadge = c.restarts > 0
      ? `<span class="svc-badge unhealthy">R:${c.restarts}</span>` : '';
    col1 += `<div class="svc-row">
      <span class="svc-dot ${dotClass}"></span>
      <span class="svc-name">${esc(c.name)}</span>
      ${c.port ? `<span class="svc-port">:${c.port}</span>` : ''}
      ${badge}
      ${restartBadge}
      <span class="svc-detail">${c.uptime}</span>
    </div>`;
  }
  col1 += '</div>';

  // Column 2: Tunnels
  let col2 = '<div class="svc-col"><div class="svc-col-hdr">TUNNELS</div>';
  for (const t of (data.tunnels || [])) {
    col2 += `<div class="svc-row ${t.status === 'down' ? 'down' : ''}">
      <span class="svc-dot ${t.status}"></span>
      <span class="svc-name">${esc(t.name)}</span>
      <span class="svc-latency">${t.status === 'up' ? t.latency_ms + 'ms' : 'DOWN'}</span>
    </div>`;
    col2 += `<div class="svc-sub">${esc(t.hostname)}</div>`;
  }
  col2 += '</div>';

  // Column 3: Sync + Chezmoi
  const sync = data.sync || {};
  const chez = data.chezmoi || {};
  let col3 = '<div class="svc-col"><div class="svc-col-hdr">SYNC</div>';
  const conflictBadge = sync.conflicts > 0
    ? `<span class="svc-badge unhealthy">${sync.conflicts} conflict${sync.conflicts > 1 ? 's' : ''}</span>` : '';
  col3 += `<div class="svc-row">
    <span class="svc-dot ${sync.connected ? 'up' : 'down'}"></span>
    <span class="svc-name">Syncthing</span>
    <span class="svc-detail">${sync.connected ? 'LINKED' : 'OFFLINE'}</span>
    ${conflictBadge}
  </div>`;
  for (const f of (sync.folders || [])) {
    const inSync = f.need_files === 0;
    const pct = inSync ? 100 : (f.files > 0 ? Math.round((f.files - f.need_files) / f.files * 100) : 0);
    col3 += `<div class="sync-folder">
      <div class="sync-top">
        <span class="svc-dot ${inSync ? 'idle' : 'syncing'}"></span>
        <span class="svc-name">${esc(f.id)}</span>
        <span class="svc-detail">${f.state}</span>
      </div>
      <div class="sync-bar"><div class="sync-fill" style="width:${pct}%"></div></div>
      <div class="sync-meta">
        <span>${fmtK(f.files)} files \u00b7 ${f.size_gb.toFixed(1)}GB</span>
        <span>${f.need_files > 0 ? f.need_files + ' pending' : timeAgo(f.last_scan)}</span>
      </div>
    </div>`;
  }
  if (chez.last_commit) {
    col3 += '<div class="svc-col-hdr" style="margin-top:6px">DOTFILES</div>';
    col3 += `<div class="svc-row">
      <span class="svc-dot up"></span>
      <span class="svc-name">chezmoi</span>
      <span class="svc-detail">${chez.modified || 0}M ${chez.added || 0}A</span>
    </div>`;
    col3 += `<div class="svc-sub">${esc(chez.last_commit)}</div>`;
  }
  col3 += '</div>';

  el.innerHTML = col1 + col2 + col3;
}
