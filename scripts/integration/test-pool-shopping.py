#!/usr/bin/env python3
"""Called by the real validator test while its two native nodes remain alive.
Restores actual earned records; does not manufacture stake, rewards or fees.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

shop = Path(__file__).resolve().parents[2]
pool = Path(os.environ["NIMIQBASE_CHECKOUT"]).resolve() / "pool"
go = os.environ.get("NIMSHOP_GO_BIN") or shutil.which("go")
fixture = Path(sys.argv[1]).resolve()
output = shop / "test-results" / "pool-shopping"
output.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory(prefix=".earned-api-", dir=pool) as folder:
    task = Path(folder)
    shutil.copyfile(Path(__file__).with_name("pool-earned-api.go.txt"), task / "main.go")
    subprocess.run([go, "build", "-o", str(task / "fixture"), "./" + task.name], cwd=pool, check=True)
    server = subprocess.Popen([str(task / "fixture"), str(fixture)], stdout=subprocess.PIPE, text=True)
    try:
        url = server.stdout.readline().strip()
        if not url.startswith("http://127.0.0.1:"):
            raise RuntimeError("actual earned API failed to start")
        data = json.loads(fixture.read_text())
        data["pool_url"] = url
        fixture.write_text(json.dumps(data))
        env = dict(os.environ, NIMSHOP_POOL_FLOW_FIXTURE=str(fixture), NIMSHOP_POOL_FLOW_OUTPUT=str(output), NIMSHOP_FLOW_ROOT=str(shop))
        # Debug retries retain the already earned native fixture.
        for attempt in range(1, 20):
            log = output / f"shop-{attempt}.log"
            with log.open("w") as stream:
                result = subprocess.run([go, "test", "-race", "./cmd/server", "-run", "^TestLivePoolShoppingCashback$", "-v", "-count=1", "-timeout=12m"], cwd=shop / "backend", env=env, stdout=stream, stderr=subprocess.STDOUT)
            if result.returncode == 0 and "SHOP FLOW VERIFIED:" in log.read_text():
                print(log.read_text(), flush=True)
                break
            if os.environ.get("NIMSHOP_FLOW_DEBUG_RETRY") != "1":
                raise RuntimeError(log.read_text())
            print(f"Connected shop attempt failed: {log}; waiting for retry file", flush=True)
            import time
            deadline = time.monotonic() + 600
            retry = output / "retry"
            while not retry.exists():
                if time.monotonic() > deadline:
                    raise RuntimeError(log.read_text())
                time.sleep(1)
            retry.unlink()
        else:
            raise RuntimeError("shop flow never completed")
    finally:
        server.terminate()
        server.wait(timeout=10)
