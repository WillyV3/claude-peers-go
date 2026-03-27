// ========== PAGE 3: NATS ==========

function updateNATSPage(data) {
  const el = document.getElementById('nats-layout');
  if (!el || !data || !data.timestamp) return;

  const s = data.server || {};
  const conns = data.connections || [];
  const streams = data.streams || [];

  // Flatten all consumers across streams for the bottom-right panel.
  const allConsumers = [];
  for (const st of streams) {
    for (const c of (st.consumers || [])) {
      allConsumers.push({ ...c, stream: st.name });
    }
  }

  // Top-left: Server vitals.
  const serverHtml = `<div class="nats-server">
    <div class="nats-section-hdr">NATS SERVER</div>
    <div class="nats-kv"><span class="lbl">VERSION</span><span class="val">${esc(s.version || '?')}</span></div>
    <div class="nats-kv"><span class="lbl">UPTIME</span><span class="val">${esc(s.uptime || '?')}</span></div>
    <div class="nats-kv"><span class="lbl">CONNS</span><span class="val-hl">${s.connections || 0}</span></div>
    <div class="nats-kv"><span class="lbl">IN MSGS</span><span class="val">${fmtK(s.in_msgs || 0)}</span></div>
    <div class="nats-kv"><span class="lbl">OUT MSGS</span><span class="val">${fmtK(s.out_msgs || 0)}</span></div>
    <div class="nats-kv"><span class="lbl">IN</span><span class="val">${fB(s.in_bytes || 0)}</span></div>
    <div class="nats-kv"><span class="lbl">OUT</span><span class="val">${fB(s.out_bytes || 0)}</span></div>
    <div class="nats-kv"><span class="lbl">SLOW</span><span class="${s.slow_consumers > 0 ? 'val-err' : 'val'}">${s.slow_consumers || 0}</span></div>
    <div class="nats-kv"><span class="lbl">JS MEM</span><span class="val">${fB(s.mem_used || 0)} / ${fB(s.mem_max || 0)}</span></div>
    <div class="nats-kv"><span class="lbl">JS STORE</span><span class="val">${fB(s.store_used || 0)} / ${fB(s.store_max || 0)}</span></div>
    <div class="nats-kv"><span class="lbl">API</span><span class="val">${fmtK(s.api_total || 0)} total</span></div>
    <div class="nats-kv"><span class="lbl">API ERR</span><span class="${s.api_errors > 0 ? 'val-err' : 'val'}">${s.api_errors || 0}</span></div>
  </div>`;

  // Top-right: JetStream streams.
  let streamHtml = '<div class="nats-stream"><div class="nats-section-hdr">JETSTREAM</div>';
  if (streams.length === 0) {
    streamHtml += '<div class="nats-kv"><span class="lbl">no streams</span></div>';
  } else {
    for (const st of streams) {
      const consumerCount = (st.consumers || []).length;
      streamHtml += `<div class="nats-stream-entry">
        <div class="nats-kv"><span class="lbl">${esc(st.name)}</span><span class="val-hl">${fmtK(st.messages || 0)} msgs</span></div>
        <div class="nats-kv"><span class="lbl">SIZE</span><span class="val">${fB(st.bytes || 0)}</span></div>
        <div class="nats-kv"><span class="lbl">CONSUMERS</span><span class="val">${consumerCount}</span></div>
        ${st.first_seq !== undefined ? `<div class="nats-kv"><span class="lbl">SEQ</span><span class="val">${st.first_seq} &rarr; ${st.last_seq}</span></div>` : ''}
        ${st.subjects !== undefined ? `<div class="nats-kv"><span class="lbl">SUBJECTS</span><span class="val">${st.subjects}</span></div>` : ''}
      </div>`;
    }
  }
  streamHtml += '</div>';

  // Bottom-left: Active connections.
  let connsHtml = '<div class="nats-conns"><div class="nats-section-hdr">CONNECTIONS</div>';
  if (conns.length === 0) {
    connsHtml += '<div class="nats-kv"><span class="lbl">no active connections</span></div>';
  } else {
    for (const c of conns) {
      const name = c.name || c.client_id || 'unknown';
      const ip = (c.ip || '').replace(/^::ffff:/, '');
      const inMsgs = fmtK(c.in_msgs || 0);
      const outMsgs = fmtK(c.out_msgs || 0);
      connsHtml += `<div class="nats-conn-row">
        <div class="nats-conn-dot"></div>
        <span class="nats-conn-name">${esc(name)}</span>
        <span class="nats-conn-ip">${esc(ip)}</span>
        <span class="nats-conn-msgs">${inMsgs}/${outMsgs}</span>
      </div>`;
    }
  }
  connsHtml += '</div>';

  // Bottom-right: Consumer details.
  let consumersHtml = '<div class="nats-consumers"><div class="nats-section-hdr">CONSUMERS</div>';
  if (allConsumers.length === 0) {
    consumersHtml += '<div class="nats-kv"><span class="lbl">no consumers</span></div>';
  } else {
    for (const c of allConsumers) {
      const pending = c.num_pending !== undefined ? c.num_pending : (c.ack_pending || 0);
      const pendingClass = pending === 0 ? 'ok' : pending < 10 ? 'warn' : 'bad';
      const delivered = c.delivered || c.num_delivered || 0;
      consumersHtml += `<div class="nats-consumer-row">
        <span class="nats-consumer-name">${esc(c.name || c.consumer_name || '?')}</span>
        <span class="nats-consumer-stream">${esc(c.stream)}</span>
        <span class="nats-consumer-pending ${pendingClass}">${pending === 0 ? 'OK' : pending + ' pend'}</span>
        <span class="nats-conn-msgs">${fmtK(delivered)}</span>
      </div>`;
    }
  }
  consumersHtml += '</div>';

  el.innerHTML = serverHtml + streamHtml + connsHtml + consumersHtml;
}
