"""
Generates golden.json for the Go parser tests from the *Python* reference
implementation (ai-log-incident-analyzer/app/parser.py), so the Go port is
checked against it byte for byte - same normalized text and same SHA-256,
which is what makes the two apps dedup against each other on a shared DB.

Usage (from the Python repo, so `app` and `data` are importable):
    PYTHONPATH=. uv run python <path-to-this-file> <path-to-golden.json>
"""
import json
import sys

from app.parser import compute_error_hash, normalize_raw_text
from data.synthetic_logs import SYNTHETIC_LOGS

# Realistic inputs only: the Go parser uses Go's own notion of whitespace and line
# breaks, so exotic characters (form feed, NEL, a lone \r, Unicode spaces) are
# deliberately not part of the parity check.
cases = [{"name": f"synthetic/{i:02d}-{e.service}", "raw": e.raw_text, "max_frames": 5}
         for i, e in enumerate(SYNTHETIC_LOGS)]

many_frames = "ValueError: bad thing\n" + "\n".join(
    f"  at pkg.mod.func{i}(file{i}.py:{i})" for i in range(10))
py_trace = (
    'Traceback (most recent call last):\n'
    '  File "a.py", line 1, in f\n    x = 1\n'
    '  File "b.py", line 2, in g\n    y = 2\n'
    '  File "c.py", line 3, in h\n    z = 3\n'
    'KeyError: \'k\'\n')
edge = {
    "edge/empty": "",
    "edge/only-whitespace": "  \n\t\n   \n",
    "edge/crlf": "boom\r\n  at a.b.C.d(C.java:1)\r\n  at e.f.G.h(G.java:2)\r\n",
    "edge/many-frames-truncated": many_frames,
    "edge/python-source-echo-dropped": py_trace,
    "edge/leading-trailing-newlines": "\n\n  ERROR: x\n\n",
    "edge/unicode-text": "Greška: šđčćž — 日本語\n  at x.Y.z(Y.java:9)",
    "edge/go-panic": "panic: x\n\ngoroutine 1 [running]:\nmain.f()\n\t/app/main.go:10 +0x1b\n",
    "edge/bare-file-line": "oops\nserver.go:77\nhandler.kt:12 extra",
    "edge/at-no-space": "at\nattempt failed\nat x",
}
for name, raw in edge.items():
    cases.append({"name": name, "raw": raw, "max_frames": 5})
cases.append({"name": "edge/max-frames-2", "raw": many_frames, "max_frames": 2})
cases.append({"name": "edge/max-frames-0", "raw": many_frames, "max_frames": 0})

for c in cases:
    c["normalized"] = normalize_raw_text(c["raw"], c["max_frames"])
    c["hash"] = compute_error_hash(c["normalized"])

with open(sys.argv[1], "w", encoding="utf-8") as f:
    json.dump(cases, f, ensure_ascii=False, indent=1)
print(f"wrote {len(cases)} cases")
