#!/usr/bin/env python3
"""Run shop client + credit tests against the actual NimiqBase API in memory.
Usage: NIMSHOP_GO_BIN=/path/to/go python scripts/integration/test-pool-contract.py /path/to/NimiqBase
No production database, feed secret, wallet seed, or chain transaction is used.
"""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

shop = Path(__file__).resolve().parents[2]
pool = Path(sys.argv[1]).resolve() / "pool"
go = os.environ.get("NIMSHOP_GO_BIN") or shutil.which("go")
if not go or not (pool / "go.mod").is_file():
    raise SystemExit("Go and a checkout of NimiqBase/pool are required")
with tempfile.TemporaryDirectory(prefix=".contract-fixture-", dir=pool) as task:
    task_path = Path(task)
    shutil.copyfile(Path(__file__).with_name("pool-contract-fixture.go.txt"), task_path / "main.go")
    binary = task_path / "fixture"
    subprocess.run([go, "build", "-o", str(binary), "./" + task_path.name], cwd=pool, check=True)
    server = subprocess.Popen([str(binary)], stdout=subprocess.PIPE, text=True)
    try:
        url = server.stdout.readline().strip()
        # This integration fixture must stay on loopback; reject remote endpoints.
        if not url.startswith("http://127.0.0.1:"):  # DevSkim: ignore DS162092
            raise RuntimeError("local fixture did not start")
        env = dict(os.environ, POOL_LIVE_URL=url, POOL_LIVE_FEED_KEY="contract-fixture-only",
                   POOL_LIVE_FUNDED_ADDRESS="NQ20 TSB0 DFSM UH9C 15GQ GAGJ TTE4 D3MA 859E",
                   POOL_LIVE_EXPECT_FEE_LUNA="1000000")
        subprocess.run([go, "test", "-race", "./internal/poolstake", "-run", "^TestLivePool", "-count=1", "-v"],
                       cwd=shop / "backend", env=env, check=True, timeout=90)
    finally:
        server.terminate()
        server.wait(timeout=10)
