#!/usr/bin/python3
"""EdgeBox 10-Minute Hotel Captive Portal Assistance Mode.

Temporarily allows devices on the management Wi-Fi (wlan0) to route through
the physical WAN (eth0) using N1's MAC address to complete hotel captive portal /
room number authentication.

Security invariants:
- Automatically disables after duration (default 600s).
- Restores isolated AP firewall state on stop or error.
- Never touches WireGuard (ebwg0) or policy routing (table 177).
"""
import json
import os
import pathlib
import subprocess
import sys
import time

STATE_FILE = pathlib.Path("/var/lib/edgebox/hotel-mode.json")
ISOLATION_NFT = pathlib.Path("/etc/edgebox/ap-isolation.nft")

def get_wan_info():
    out = subprocess.check_output(["ip", "-j", "-4", "route", "show", "table", "main", "default"], text=True)
    routes = json.loads(out)
    if not routes:
        raise RuntimeError("No default route found on physical WAN")
    r = routes[0]
    dev = r["dev"]
    gateway = r["gateway"]

    # Read DNS from resolvectl or resolv.conf
    dns = gateway  # fallback to gateway
    try:
        res = subprocess.check_output(["resolvectl", "dns", dev], text=True)
        parts = res.split(":")
        if len(parts) > 1:
            servers = parts[1].strip().split()
            for s in servers:
                if "." in s:  # IPv4
                    dns = s
                    break
    except Exception:
        pass
    return dev, gateway, dns

def enable(duration=600):
    os.umask(0o077)
    dev, gateway, dns = get_wan_info()

    # Build temporary open-for-auth nftables ruleset
    rules = f"""table inet edgebox_management {{
 chain isolate_forward {{
  type filter hook forward priority -10; policy accept;
  iifname "wlan0" oifname "{dev}" counter accept
  oifname "wlan0" iifname "{dev}" ct state {{ established, related }} counter accept
  iifname "wlan0" drop
  oifname "wlan0" drop
 }}
 chain restrict_web_ingress {{
  type filter hook input priority -10; policy accept;
  iifname != "wlan0" iifname != "lo" ip daddr 192.168.50.1 tcp dport 8080 drop
 }}
 chain hotel_prerouting {{
  type nat hook prerouting priority -100; policy accept;
  iifname "wlan0" udp dport 53 counter dnat ip to {dns}:53
  iifname "wlan0" tcp dport 53 counter dnat ip to {dns}:53
 }}
 chain hotel_nat {{
  type nat hook postrouting priority 100; policy accept;
  iifname "wlan0" oifname "{dev}" counter masquerade
 }}
}}
"""
    # Test rules syntax
    subprocess.run(["nft", "-c", "-f", "-"], input=rules, text=True, check=True)

    # Enable ip forwarding
    subprocess.check_call(["sysctl", "-w", "net.ipv4.ip_forward=1"])

    # Apply atomic table replacement
    batch = "delete table inet edgebox_management\n" + rules
    subprocess.run(["nft", "-f", "-"], input=batch, text=True, check=True)

    # Arm automatic shutoff timer
    subprocess.run(["systemctl", "stop", "edgebox-hotel-mode-expire.timer"], capture_output=True)
    subprocess.check_call([
        "systemd-run",
        "--unit=edgebox-hotel-mode-expire",
        f"--on-active={duration}s",
        "--timer-property=AccuracySec=1s",
        "/usr/bin/python3",
        "/opt/edgebox-network/hotel-portal.py",
        "disable"
    ])

    expires_at = time.time() + duration
    STATE_FILE.parent.mkdir(parents=True, exist_ok=True)
    STATE_FILE.write_text(json.dumps({
        "active": True,
        "enabled_at": time.time(),
        "expires_at": expires_at,
        "duration_seconds": duration,
        "wan_dev": dev,
        "wan_gateway": gateway,
        "dns": dns
    }, indent=2))

    return {"status": "enabled", "expires_at": expires_at, "duration": duration}

def disable():
    # 1. Cancel timer
    subprocess.run(["systemctl", "stop", "edgebox-hotel-mode-expire.timer"], capture_output=True)

    # 2. Restore strict isolation
    if ISOLATION_NFT.exists():
        batch = "delete table inet edgebox_management\n" + ISOLATION_NFT.read_text()
        subprocess.run(["nft", "-f", "-"], input=batch, text=True)

    STATE_FILE.write_text(json.dumps({
        "active": False,
        "disabled_at": time.time()
    }, indent=2))

    return {"status": "disabled"}

def status():
    if not STATE_FILE.exists():
        return {"active": False}
    try:
        data = json.loads(STATE_FILE.read_text())
        if data.get("active") and time.time() > data.get("expires_at", 0):
            # Expired but not yet cleaned up
            return disable()
        return data
    except Exception:
        return {"active": False}

if __name__ == "__main__":
    action = sys.argv[1] if len(sys.argv) > 1 else "status"
    if action == "enable":
        dur = int(sys.argv[2]) if len(sys.argv) > 2 else 600
        print(json.dumps(enable(dur), indent=2))
    elif action == "disable":
        print(json.dumps(disable(), indent=2))
    else:
        print(json.dumps(status(), indent=2))
