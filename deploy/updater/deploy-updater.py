#!/usr/bin/env python3
"""Deploy updated edge-updater binary and service unit to N1 with timed rollback."""
import json
import os
import pathlib
import shutil
import subprocess
import sys
import time

STAGE_DIR = pathlib.Path("/root/edgebox-phase3-updater")
BACKUP_DIR = pathlib.Path("/var/lib/edgebox/backup/phase3-baseline")
UPDATER_BIN = pathlib.Path("/usr/local/libexec/edge-updater")
UPDATER_SVC = pathlib.Path("/etc/systemd/system/edge-updater.service")
CONTROL_SOCK = "/run/edgebox-updater/control.sock"

def query_status():
    cmd = [
        "curl", "--fail", "--silent", "--max-time", "5",
        "--unix-socket", CONTROL_SOCK,
        "--data", json.dumps({"kind": "update_status"}),
        "http://local/action"
    ]
    res = subprocess.run(cmd, capture_output=True, text=True)
    if res.returncode != 0:
        return None
    return json.loads(res.stdout)

def main():
    action = sys.argv[1] if len(sys.argv) > 1 else "deploy"

    if action == "rollback":
        print("=== EXECUTING TIMED ROLLBACK FOR EDGE-UPDATER ===")
        if (STAGE_DIR / "edge-updater.bak").exists():
            subprocess.run(["install", "-o", "root", "-g", "root", "-m", "755", str(STAGE_DIR / "edge-updater.bak"), str(UPDATER_BIN)])
        if (STAGE_DIR / "edge-updater.service.bak").exists():
            shutil.copy2(STAGE_DIR / "edge-updater.service.bak", UPDATER_SVC)
        subprocess.run(["systemctl", "daemon-reload"])
        subprocess.run(["systemctl", "restart", "edge-updater.service"])
        print("Rollback complete.")
        return

    if action == "deploy":
        src_bin = STAGE_DIR / "edge-updater-arm64"
        if not src_bin.exists():
            print(f"Error: {src_bin} does not exist", file=sys.stderr)
            sys.exit(1)

        STAGE_DIR.mkdir(parents=True, exist_ok=True)
        BACKUP_DIR.mkdir(parents=True, exist_ok=True)

        # 1. Backup current binary and service
        shutil.copy2(UPDATER_BIN, STAGE_DIR / "edge-updater.bak")
        shutil.copy2(UPDATER_BIN, BACKUP_DIR / "edge-updater.orig")
        shutil.copy2(UPDATER_SVC, STAGE_DIR / "edge-updater.service.bak")
        shutil.copy2(UPDATER_SVC, BACKUP_DIR / "edge-updater.service.orig")

        # 2. Schedule timed rollback (3 minutes)
        print("Scheduling 180s timed rollback...")
        subprocess.run(["systemctl", "stop", "edgebox-updater-rollback.timer"], capture_output=True)
        subprocess.check_call([
            "systemd-run",
            "--unit=edgebox-updater-rollback",
            "--on-active=180s",
            "--timer-property=AccuracySec=1s",
            "/usr/bin/python3",
            str(pathlib.Path(__file__).resolve()),
            "rollback"
        ])

        # 3. Update service file with /var/lib/edgebox in ReadWritePaths
        svc_text = UPDATER_SVC.read_text()
        if "ReadWritePaths=/opt/edgebox /var/lib/edgebox/ap" in svc_text:
            svc_text = svc_text.replace(
                "ReadWritePaths=/opt/edgebox /var/lib/edgebox/ap /var/lib/edgebox/updates /etc/NetworkManager/system-connections /etc/systemd/system",
                "ReadWritePaths=/opt/edgebox /var/lib/edgebox /etc/NetworkManager/system-connections /etc/systemd/system"
            )
            UPDATER_SVC.write_text(svc_text)
            print("Updated edge-updater.service ReadWritePaths to include /var/lib/edgebox")

        # 4. Install new binary using install command (unlinks old inode so no ETXTBSY)
        subprocess.check_call(["install", "-o", "root", "-g", "root", "-m", "755", str(src_bin), str(UPDATER_BIN)])
        print("Installed new edge-updater binary")

        # 5. Reload and restart
        subprocess.check_call(["systemctl", "daemon-reload"])
        subprocess.check_call(["systemctl", "restart", "edge-updater.service"])

        # 6. Verify health
        time.sleep(2)
        healthy = False
        for i in range(10):
            st = query_status()
            if st and "state" in st:
                print(f"edge-updater responding: phase={st.get('state', {}).get('phase')}, current={st.get('state', {}).get('current')}")
                healthy = True
                break
            time.sleep(1)

        if not healthy:
            print("Failed to verify edge-updater! Triggering immediate rollback...")
            subprocess.run(["python3", str(pathlib.Path(__file__).resolve()), "rollback"])
            sys.exit(1)

        # 7. Cancel rollback timer
        subprocess.run(["systemctl", "stop", "edgebox-updater-rollback.timer"])
        print("Deployment verified. Timed rollback disarmed.")

if __name__ == "__main__":
    main()
