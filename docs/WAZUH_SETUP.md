# Wazuh EDR Setup

How to deploy Wazuh endpoint detection and response for the Sontara Lattice fleet.

## Overview

Wazuh provides continuous security monitoring: file integrity, auth log analysis, process monitoring, and network anomaly detection. The `wazuh-bridge` component tails Wazuh's alert output and publishes structured security events to NATS, where the trust broker uses them to dynamically adjust machine trust levels.

## Architecture

```
Wazuh Agent (each machine)
  -> reports to Wazuh Manager (Docker on broker machine)
    -> writes alerts to alerts.json
      -> wazuh-bridge tails alerts.json
        -> publishes to NATS fleet.security.*
          -> broker updates machine health scores
            -> gridwatch shows security status
```

## Deploy Wazuh Manager

On the broker machine (Docker required):

```bash
mkdir -p ~/docker/wazuh
cd ~/docker/wazuh

# Create .env
echo "WAZUH_API_PASSWORD=YourSecurePassword123!" > .env

# Create docker-compose.yml
cat > docker-compose.yml << 'EOF'
services:
  wazuh-manager:
    image: wazuh/wazuh-manager:4.14.4
    hostname: wazuh.manager
    container_name: wazuh-manager
    restart: unless-stopped
    ports:
      - "1514:1514"
      - "1515:1515"
      - "514:514/udp"
      - "55000:55000"
    environment:
      - INDEXER_URL=https://127.0.0.1:9200
      - FILEBEAT_SSL_VERIFICATION_MODE=none
      - API_USERNAME=wazuh-api
      - API_PASSWORD=${WAZUH_API_PASSWORD}
    volumes:
      - wazuh_api_configuration:/var/ossec/api/configuration
      - wazuh_etc:/var/ossec/etc
      - wazuh_queue:/var/ossec/queue
      - wazuh_var_multigroups:/var/ossec/var/multigroups
      - wazuh_integrations:/var/ossec/integrations
      - wazuh_active_response:/var/ossec/active-response/bin
      - wazuh_agentless:/var/ossec/agentless
      - wazuh_wodles:/var/ossec/wodles
      - ./logs:/var/ossec/logs
    deploy:
      resources:
        limits:
          memory: 2G

volumes:
  wazuh_api_configuration:
  wazuh_etc:
  wazuh_queue:
  wazuh_var_multigroups:
  wazuh_integrations:
  wazuh_active_response:
  wazuh_agentless:
  wazuh_wodles:
EOF

docker compose up -d
```

Fix log permissions so the bridge can read alerts:
```bash
docker exec wazuh-manager chmod -R o+r /var/ossec/logs/alerts/
docker exec wazuh-manager chmod o+x /var/ossec/logs /var/ossec/logs/alerts
```

## Deploy custom rules

Copy the Sontara Lattice custom rules into the manager:

```bash
docker cp local_rules.xml wazuh-manager:/var/ossec/etc/rules/local_rules.xml
docker exec wazuh-manager /var/ossec/bin/wazuh-control restart
```

Custom rules detect:
| Rule ID | Level | What it detects |
|---------|-------|----------------|
| 100100 | 12 | UCAN credential file modified (identity.pem, token.jwt, root.pub) |
| 100101 | 13 | claude-peers binary tampered |
| 100102 | 10 | SSH key or config modified |
| 100130 | 9 | Systemd unit file changed |
| 100200 | 15 | Binary tamper + credential change on same host (QUARANTINE) |

## Deploy shared agent config

Push file integrity monitoring config to all agents via the manager:

```bash
docker exec wazuh-manager bash -c 'cat > /var/ossec/etc/shared/default/agent.conf << AGENTEOF
<agent_config>
  <syscheck>
    <disabled>no</disabled>
    <frequency>300</frequency>
    <scan_on_start>yes</scan_on_start>
    <alert_new_files>yes</alert_new_files>
    <directories check_all="yes" realtime="yes" report_changes="yes">/home/willy/.config/claude-peers</directories>
    <directories check_all="yes" realtime="yes">/home/willy/.ssh</directories>
    <directories check_all="yes" realtime="yes">/home/willy/.local/bin</directories>
    <directories check_all="yes">/usr/local/bin</directories>
    <directories check_all="yes">/etc/systemd/system</directories>
    <directories check_all="yes">/home/willy/.config/systemd/user</directories>
    <ignore type="sregex">.log$</ignore>
    <ignore type="sregex">.db$</ignore>
    <ignore type="sregex">.db-journal$</ignore>
  </syscheck>
</agent_config>
AGENTEOF'
```

## Install Wazuh agents

**Ubuntu/Debian (including Pi 5 ARM64):**
```bash
curl -s https://packages.wazuh.com/key/GPG-KEY-WAZUH | sudo gpg --dearmor -o /usr/share/keyrings/wazuh.gpg
echo "deb [signed-by=/usr/share/keyrings/wazuh.gpg] https://packages.wazuh.com/4.x/apt/ stable main" | sudo tee /etc/apt/sources.list.d/wazuh.list
sudo apt update
sudo WAZUH_MANAGER="<broker-tailscale-ip>" apt install -y wazuh-agent
sudo systemctl enable --now wazuh-agent
```

**Arch Linux (AUR):**
```bash
yay -S wazuh-agent
# Edit /var/ossec/etc/ossec.conf: set <address> to broker IP
sudo systemctl enable --now wazuh-agent
```

**macOS:**
```bash
curl -LO https://packages.wazuh.com/4.x/macos/wazuh-agent-4.14.4-1.arm64.pkg
sudo installer -pkg wazuh-agent-*.pkg -target /
sudo /Library/Ossec/bin/agent-auth -m <broker-tailscale-ip>
sudo /Library/Ossec/bin/wazuh-control start
```

**Pi Zero 2W (ARMv7):** Wazuh does not support ARMv6. Pi Zero 2W runs ARMv7 which should work with the ARM packages. If it doesn't, deploy a lightweight sentinel script instead.

Verify agents are connected:
```bash
docker exec wazuh-manager /var/ossec/bin/agent_control -l
```

## Start the bridge

```bash
# As a systemd service
cat > ~/.config/systemd/user/sontara-wazuh-bridge.service << EOF
[Unit]
Description=Sontara Lattice Wazuh Bridge
After=network-online.target docker.service

[Service]
ExecStart=%h/.local/bin/claude-peers wazuh-bridge
Restart=always
RestartSec=5
Environment=WAZUH_ALERTS_PATH=$HOME/docker/wazuh/logs/alerts/alerts.json
Environment=CLAUDE_PEERS_TOKEN=<fleet-write-jwt>

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now sontara-wazuh-bridge
```

## Trust demotion

Security events affect machine trust automatically:

| Wazuh Level | Severity | Health Score Impact | Trust Effect |
|-------------|----------|-------------------|-------------|
| 1-5 | Info | +0 | Log only |
| 6-9 | Warning | +1 | Accumulated warnings degrade trust |
| 10-12 | Critical | +10 | Capabilities demoted to fleet-read |
| 13-15 | Quarantine | Immediate | All capabilities revoked |

Health scores decay by 1 every 10 minutes for non-quarantined machines.

Quarantine requires manual recovery:
```bash
claude-peers unquarantine <machine-name>
```

## Verify the pipeline

```bash
# Create a test file in a monitored directory
touch ~/.config/claude-peers/test-fim-verify

# Wait ~30 seconds for FIM detection + bridge processing

# Check bridge logs
journalctl --user -u sontara-wazuh-bridge -n 10

# Check machine health
curl -H "Authorization: Bearer $(cat ~/.config/claude-peers/token.jwt)" \
  http://<broker-ip>:7899/machine-health | python3 -m json.tool

# Clean up
rm ~/.config/claude-peers/test-fim-verify
```
