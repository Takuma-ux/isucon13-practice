# Profile-Guided Optimization (PGO)

> **CHECK `default.pgo.meta` BEFORE COMPETITION**  
> The committed **`default.pgo`** is an **interim / synthetic** profile from **local `loadbench` traffic on 2026-10-21**, **not** from real competition benchmark traffic.  
> **`go build` succeeds either way** — a wrong or stale profile fails silently and only skews optimization.  
> **You MUST regenerate `default.pgo` from a real bench run’s pprof capture** (steps below) before trusting PGO on competition day. Update `default.pgo.meta` when you replace the file.

Go **1.20+** (this repo: **1.22**) uses `default.pgo` in the **main package directory** (`go/`) automatically when you run `go build` in that package (`-pgo=auto` is default).

## Capture during a real bench run (preferred)

On the **competition app server** while the official bench (or a full-strength rehearsal) is running:

```bash
# CPU profile (30s wall; run while bench load is active)
curl -sS -o /tmp/isucon-cpu.prof \
  'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'

# Convert to compiler input (protobuf profile)
go tool pprof -proto -output=default.pgo /tmp/isucon-cpu.prof

# Rebuild app on the server or copy default.pgo into go/ before CI/build
cd /home/isucon/webapp/go   # or your checkout path
mv default.pgo ./default.pgo
go build -o isupipe .
```

Copy `default.pgo` back to your practice repo if you build off-machine.

**pprof** is already started in `main.go` on **`127.0.0.1:6060`** (`net/http/pprof`). Only reachable from the host (not public) unless you change the bind address.

## Local interim profile (loadbench)

When you do not have a bench `.prof` file:

```bash
./scripts/capture-pgo.sh
```

This starts the app (if not running), runs `cmd/loadbench` for ~45s, captures a overlapping CPU profile from `:6060`, and writes `go/default.pgo`.

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
