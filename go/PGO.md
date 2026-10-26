# Profile-Guided Optimization (PGO)

> **CHECK `default.pgo.meta` BEFORE COMPETITION**  
> The committed **`default.pgo`** is an **interim / synthetic** profile from **local `loadbench` traffic on 2026-10-21**, **not** from real competition benchmark traffic.  
> **`go build` succeeds either way** — a wrong or stale profile fails silently and only skews optimization.  
> **You MUST regenerate `default.pgo` from a real bench run’s pprof capture** (steps below) before trusting PGO on competition day. Update `default.pgo.meta` when you replace the file.

Go **1.20+** (this repo: **1.22**) uses `default.pgo` in the **main package directory** (`go/`) automatically when you run `go build` in that package (`-pgo=auto` is default).

## Capture during a real bench run (preferred)

On the **competition app server**, run this **immediately after a bench** (or during the last ~30s of load) while isupipe is still up:

```bash
# From repo root on the app server
PGO_CAPTURE_SOURCE="real bench run" \
PGO_BENCH_TARGET="http://pipe.u.isucon.local:8080" \
  ./go/scripts/rebuild-pgo-from-profile.sh
```

One command: CPU profile from `:6060` → `go/default.pgo` → `default.pgo.meta` → `go build -o ../isupipe` with `-pgoprofile`. Then restart the app and run the **next** bench against the PGO binary.

Manual equivalent:

```bash
curl -sS -o /tmp/isucon-cpu.prof 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'
go tool pprof -proto -output=go/default.pgo /tmp/isucon-cpu.prof
cd go && go build -o ../isupipe .
```

Copy `default.pgo` back to your practice repo if you build off-machine.

**pprof** is already started in `main.go` on **`127.0.0.1:6060`** (`net/http/pprof`). Only reachable from the host (not public) unless you change the bind address.

## Local interim profile (loadbench)

When you do not have a bench `.prof` file:

```bash
./scripts/capture-pgo.sh
```

This starts the app (if not running), runs `cmd/loadbench` (default **120s**, **60s** warmup before capture), captures a **30s** CPU profile from `:6060`, and writes `go/default.pgo`. Override with `LOAD_DURATION`, `PGO_WARMUP_SECONDS`, `PGO_PROFILE_SECONDS`, `LOAD_WORKERS`.

Interim profiles are **less representative** than a real bench (no DNS torture, different concurrency), but better than no PGO.

## Verify PGO was used

```bash
cd go
go build -x -o /tmp/isupipe-pgo-check . 2>&1 | grep -i pgoprofile
```

You should see compile lines containing **`-pgoprofile=.../default.pgo`**. If `default.pgo` is missing, the grep is empty (build still succeeds; PGO off).

To disable: `go build -pgo=off` or remove/rename `default.pgo`.

## Regenerating

Replace `default.pgo` after major handler changes or when you have a new bench profile. **Always update `default.pgo.meta`** with source (bench vs loadbench), date, and host so the next person is not misled.

Commit an updated `default.pgo` only if the team agrees (binary-ish artifact; can be large).
