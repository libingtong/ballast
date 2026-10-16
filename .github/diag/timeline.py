import json, sys, glob, os, collections
root = sys.argv[1]
sess = sys.argv[2]
rows = []
files = []
for dp, dn, fn in os.walk(root):
    if os.path.basename(dp) == "audit":
        files += [os.path.join(dp, x) for x in fn if x.endswith(".jsonl")]
for f in files:
    for line in open(f, encoding="utf8"):
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("session_id") != sess:
            continue
        d = r.get("detail") or {}
        rows.append((r.get("ts", ""), r.get("type"), d.get("diag", ""), d.get("wait_us", ""), r.get("event_seq", "")))
rows.sort()
print("session", sess, "records", len(rows))
t0 = None
from datetime import datetime
def parse(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))
for ts, typ, diag, wait, seq in rows:
    t = parse(ts)
    if t0 is None:
        t0 = t
    print("%8.1fms %-5s %-13s wait_us=%-6s seq=%s" % ((t - t0).total_seconds() * 1000, typ, diag, wait, seq))
