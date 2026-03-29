#!/bin/bash
# Fleet Memory: only run if there are active peers or recent broker events.
TOKEN=$(cat ~/.config/claude-peers/token.jwt 2>/dev/null)
BROKER="http://100.109.211.128:7899"

# Check peer count
peers=$(curl -sf -H "Authorization: Bearer $TOKEN" -X POST "$BROKER/list-peers" -H 'Content-Type: application/json' -d '{"scope":"all","cwd":"/"}' 2>/dev/null | python3 -c 'import sys,json; print(len(json.load(sys.stdin)))' 2>/dev/null)
[ "${peers:-0}" -gt 0 ] && echo "$peers active peers" && exit 0

# Check recent events (last 10 min)
events=$(curl -sf -H "Authorization: Bearer $TOKEN" "$BROKER/events?limit=5" 2>/dev/null | python3 -c '
import sys,json,time
evts = json.load(sys.stdin)
recent = [e for e in evts if time.time() - time.mktime(time.strptime(e["created_at"][:19], "%Y-%m-%dT%H:%M:%S")) < 600]
print(len(recent))
' 2>/dev/null)
[ "${events:-0}" -gt 0 ] && echo "$events recent events" && exit 0

echo "no activity"
exit 1
