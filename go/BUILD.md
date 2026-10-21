# Build-time CPU target (GOAMD64)

The competition **EC2 instance type is not fixed** in this repo (no c6i/m5/etc. in handoff docs). A wrong microarchitecture level can make the binary **fail to start** (`Illegal instruction`).

## Recommended default: **GOAMD64=v2**

Safe on essentially all **x86_64 AWS EC2** instances from the last **8+ years** (Nehalem-class baseline: SSE3, CMPXCHG16, LAHF/SAHF, POPCNT).

```bash
cd go
GOAMD64=v2 go build -o isupipe .
```

## Optional upgrade: **GOAMD64=v3** (AVX2, BMI1, BMI2, FMA, …)

Only use after confirming the **app server** supports AVX2:

```bash
grep -m1 -o 'avx2' /proc/cpuinfo && echo "v3 likely OK" || echo "stay on v2"
```

Typical families with AVX2: **c5/c6i/c7i, m5/m6i/m7i, r5/r6i**, many **t3** (not guaranteed for every SKU). **Do not assume** without checking the live host.

```bash
GOAMD64=v3 go build -o isupipe .
```

## Verify what you built

```bash
go version -m ./isupipe | grep -E 'GOAMD64|goversion'
```

## PGO

See [PGO.md](./PGO.md). `default.pgo` in this directory is picked up automatically by `go build` when present.
