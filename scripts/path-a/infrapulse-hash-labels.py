#!/usr/bin/env python3
"""infrapulse-hash-labels.py: pseudonymize an InfraPulse export before you send it.

Reads the .json.gz files written by infrapulse-export.sh and writes copies in which
host names, GPU UUIDs, namespaces, pods, containers and job IDs are replaced by
HMAC-SHA256 pseudonyms. Every other label that is not needed for the analysis
(instance addresses, node names, cluster names, pod UIDs, serial numbers, ...) is
removed. Metric values and timestamps are not changed.

It uses the same method as ipcollect --salt, so with the same salt the IDs here
match the IDs in files collected by ipcollect and in a job table you hash the same way.

    export IPCOLLECT_SALT='a long secret you keep'
    python3 infrapulse-hash-labels.py infrapulse-export infrapulse-export-hashed

A job table (CSV with the columns job_id, host, gpu_index, start, end) is hashed the
same way, so it still joins with the metrics:

    python3 infrapulse-hash-labels.py --job-table jobs.csv jobs-hashed.csv

Keep the salt: you need it to map a pseudonym back to a real host. We never need it.
Python 3.6 or later, standard library only.
"""
import csv, gzip, hashlib, hmac, json, os, sys

# label -> (prefix used by ipcollect, label name written out)
HASHED = {
    "Hostname": ("host", "Hostname"), "hostname": ("host", "Hostname"),
    "UUID": ("gpu", "UUID"),
    "namespace": ("ns", "namespace"), "pod": ("pod", "pod"), "container": ("ctr", "container"),
    "job_id": ("job", "job_id"), "hpc_job": ("job", "hpc_job"), "slurm_job_id": ("job", "slurm_job_id"),
}
# When Prometheus renames the exporter's own labels on a clash (honor_labels: false),
# exported_pod is the workload and pod is the dcgm-exporter pod itself.
EXPORTED = {"exported_namespace": "namespace", "exported_pod": "pod", "exported_container": "container"}
# Kept as they are: needed for the analysis and not identifying.
KEPT = {"__name__", "gpu", "device", "modelName", "DCGM_FI_DRIVER_VERSION", "DCGM_FI_SYSTEM_DRIVER_VERSION",
        "GPU_I_ID", "GPU_I_PROFILE", "GPU_CI_ID",
        # dcgm-exporter's own counters: the XID code or clock event a series counts, and its time window
        "xid", "clock_event", "window_size_in_ms"}


def pseudonym(salt, prefix, value):
    """prefix + "_" + first 12 hex of HMAC-SHA256(salt, prefix NUL value); empty stays empty."""
    if value == "":
        return value
    mac = hmac.new(salt, digestmod=hashlib.sha256)
    mac.update(prefix.encode()); mac.update(b"\0"); mac.update(value.encode())
    return prefix + "_" + mac.hexdigest()[:12]


def clean(labels, salt, dropped):
    for src, dst in EXPORTED.items():
        if src in labels:
            labels[dst] = labels.pop(src)
    out = {}
    for name, value in labels.items():
        if name in KEPT:
            out[name] = value
        elif name in HASHED:
            prefix, to = HASHED[name]
            out[to] = pseudonym(salt, prefix, value)
        else:
            dropped[name] = dropped.get(name, 0) + 1
    return out


def job_table(src, dst, salt):
    with open(src, newline="", encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
        cols = list(rows[0].keys()) if rows else []
    missing = [c for c in ("job_id", "host") if c not in cols]
    if missing:
        sys.stderr.write("%s has no %s column; expected job_id, host, gpu_index, start, end. Nothing was written.\n" % (src, " or ".join(missing)))
        return 1
    for r in rows:
        r["job_id"] = pseudonym(salt, "job", (r["job_id"] or "").strip())
        r["host"] = pseudonym(salt, "host", (r["host"] or "").strip())
    with open(dst, "w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=cols)
        w.writeheader(); w.writerows(rows)
    print("Wrote %d jobs to %s with job_id and host hashed. Other columns are unchanged." % (len(rows), dst))
    return 0


def main(argv):
    table = len(argv) == 4 and argv[1] == "--job-table"
    if not table and (len(argv) != 3 or argv[1].startswith("-")):
        sys.stderr.write(__doc__)
        return 2
    src, dst = argv[-2], argv[-1]
    salt = os.environ.get("IPCOLLECT_SALT", "")
    if not salt:
        sys.stderr.write("Set IPCOLLECT_SALT to your secret salt first (the same one on every run).\n")
        return 2
    if table:
        if os.path.abspath(src) == os.path.abspath(dst):
            sys.stderr.write("The output file must be different from the input file.\n")
            return 2
        return job_table(src, dst, salt.encode())
    if os.path.abspath(src) == os.path.abspath(dst):
        sys.stderr.write("The output folder must be different from the input folder.\n")
        return 2
    files = sorted(f for f in os.listdir(src) if f.endswith(".json.gz"))
    if not files:
        sys.stderr.write("No .json.gz files in %s.\n" % src)
        return 1
    os.makedirs(dst, exist_ok=True)
    dropped, series = {}, 0
    for name in files:
        with gzip.open(os.path.join(src, name), "rt", encoding="utf-8") as f:
            doc = json.load(f)
        result = doc.get("data", {}).get("result")
        if doc.get("status") != "success" or not isinstance(result, list):
            sys.stderr.write("%s is not a Prometheus query result; stopping. Nothing more was written.\n" % name)
            return 1
        for s in result:
            s["metric"] = clean(s.get("metric", {}), salt.encode(), dropped)
            series += 1
        tmp = os.path.join(dst, name + ".part")
        with gzip.open(tmp, "wt", encoding="utf-8") as f:
            json.dump(doc, f, separators=(",", ":"))
        os.replace(tmp, os.path.join(dst, name))
    manifest = os.path.join(src, "MANIFEST.txt")
    if os.path.exists(manifest):
        with open(manifest) as f, open(os.path.join(dst, "MANIFEST.txt"), "w") as g:
            g.write(f.read() + "pseudonymized: yes (HMAC-SHA256, same method as ipcollect --salt)\n")
    print("Wrote %d files (%d series) to %s." % (len(files), series, dst))
    print("Hashed: host name, GPU UUID, namespace, pod, container, job ID.")
    if dropped:
        print("Removed labels: " + ", ".join(sorted(dropped)))
    print("Send %s, not %s. Keep your salt." % (dst, src))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
