#!/usr/bin/python3
"""Safe migration for N1 /etc/nftables.conf.
Removes `flush ruleset` and global `table ip n1_nat`.
Uses independent timed rollback. Run as root on N1."""
import os
import pathlib
import subprocess
import sys

NEW_CONF = """#!/usr/sbin/nft -f

# Legacy N1 tables migration: flush ruleset and broad n1_nat removed.
# EdgeBox tables are managed by edge-dataplane and edgebox-ap-isolation.
table inet n1_filter {

    chain input {
        type filter hook input priority 0;
        policy accept;
    }

    chain forward {
        type filter hook forward priority 0;
        policy accept;
    }

    chain output {
        type filter hook output priority 0;
        policy accept;
    }
}
"""

def main():
    if os.geteuid() != 0:
        sys.exit("Must run as root")

    action = sys.argv[1] if len(sys.argv) > 1 else "apply"

    conf_path = pathlib.Path("/etc/nftables.conf")
    backup_path = pathlib.Path("/var/lib/edgebox/backup/nftables.conf.pre-migration")
    rollback_script = pathlib.Path("/var/lib/edgebox/backup/rollback-nft.sh")

    if action == "apply":
        # 1. Backup
        backup_path.parent.mkdir(parents=True, exist_ok=True)
        if conf_path.exists() and not backup_path.exists():
            backup_path.write_bytes(conf_path.read_bytes())
            os.chmod(backup_path, 0o600)

        # 2. Write rollback script
        rollback_script.write_text(f"""#!/bin/sh
set -e
echo "Rolling back nftables..."
cp -f {backup_path} {conf_path}
nft -f {conf_path} || true
systemctl restart edgebox-ap-isolation edge-dataplane || true
echo "NFTABLES_ROLLED_BACK"
""")
        os.chmod(rollback_script, 0o700)

        # 3. Arm 3-minute timed rollback
        subprocess.check_call([
            "systemd-run",
            "--unit=edgebox-nft-rollback",
            "--on-active=3m",
            "--timer-property=AccuracySec=1s",
            str(rollback_script)
        ])

        # 4. Check syntax of new config
        p = subprocess.run(["nft", "-c", "-f", "-"], input=NEW_CONF, text=True, capture_output=True)
        if p.returncode != 0:
            print("Syntax check failed:", p.stderr)
            sys.exit(1)

        # 5. Write new config
        conf_path.write_text(NEW_CONF)
        os.chmod(conf_path, 0o755)

        # 6. Delete old broad NAT table if present
        subprocess.run(["nft", "delete", "table", "ip", "n1_nat"], capture_output=True)

        print("APPLY_DONE_ROLLBACK_ARMED")

    elif action == "commit":
        subprocess.run(["systemctl", "stop", "edgebox-nft-rollback.timer"], capture_output=True)
        print("MIGRATION_COMMITTED")

    elif action == "rollback":
        subprocess.run(["systemctl", "stop", "edgebox-nft-rollback.timer"], capture_output=True)
        if rollback_script.exists():
            subprocess.run([str(rollback_script)])
        print("MANUAL_ROLLBACK_DONE")

if __name__ == "__main__":
    main()
