#!/usr/bin/python3
"""HTTP(S) Time Synchronization Fallback for EdgeBox N1.
Runs when NTP is unavailable or blocked (e.g. in hotel networks).
Invariants:
- Never sets time backwards; only steps FORWARD.
- Requires two independent sources to agree within 5 seconds.
- Only steps if clock is off by >= 15 seconds or chrony is unsynchronized.
- Updates fake-hwclock so the new time persists across reboots.
"""
import email.utils
import json
import os
import subprocess
import sys
import time
import urllib.request

SOURCES = [
    "http://connectivitycheck.gstatic.com/generate_204",
    "http://www.cloudflare.com/cdn-cgi/trace",
    "http://clients3.google.com/generate_204",
    "http://captive.apple.com/hotspot-detect.html",
]

def get_source_time(url, timeout=5):
    try:
        req = urllib.request.Request(url, headers={"User-Agent": "EdgeBox-TimeSync/1.0"})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            date_hdr = resp.headers.get("Date")
            if date_hdr:
                dt = email.utils.parsedate_to_datetime(date_hdr)
                return dt.timestamp()
    except Exception:
        pass
    return None

def is_chrony_synced():
    try:
        out = subprocess.check_output(["chronyc", "tracking"], text=True, stderr=subprocess.DEVNULL)
        return "Leap status     : Normal" in out
    except Exception:
        return False

def sync_time(force_check=False):
    now = time.time()

    # If chrony is already happy and synchronized, no need to touch the clock
    if not force_check and is_chrony_synced():
        return {"status": "chrony_already_synced", "system_time": now}

    times = []
    for s in SOURCES:
        t = get_source_time(s)
        if t is not None:
            times.append((s, t))
            if len(times) >= 2:
                break

    if len(times) < 2:
        return {"status": "insufficient_sources", "sources_responding": len(times)}

    t1, t2 = times[0][1], times[1][1]
    if abs(t1 - t2) > 5.0:
        return {"status": "source_disagreement", "diff": abs(t1 - t2)}

    target_time = max(t1, t2)

    # Invariant: NEVER step backward!
    if target_time < now - 2.0:
        return {"status": "rejected_backward_step", "system_time": now, "network_time": target_time}

    # Only step if off by at least 15 seconds
    if abs(target_time - now) < 15.0:
        return {"status": "already_accurate", "offset": target_time - now}

    # Step the clock forward
    formatted = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime(target_time))
    subprocess.check_call(["date", "-u", "-s", formatted])

    # Save to fake-hwclock so next reboot starts from here
    subprocess.run(["fake-hwclock", "save"], capture_output=True)
    # Restart chrony so it recalculates drift
    subprocess.run(["systemctl", "restart", "chrony"], capture_output=True)

    return {
        "status": "stepped_forward",
        "old_time": now,
        "new_time": target_time,
        "offset_seconds": target_time - now,
        "sources": [times[0][0], times[1][0]]
    }

if __name__ == "__main__":
    force = "--check" in sys.argv or "--force" in sys.argv
    res = sync_time(force_check=force)
    print(json.dumps(res, indent=2))
