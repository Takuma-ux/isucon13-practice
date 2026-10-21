# Provisioning (ISUCON13 app server)

Scripts here tune **the machine they run on**. Use them on the **competition EC2 instance** only, not on a developer laptop (unless using dry-run).

## Safety

| Script | Risk on wrong host |
|--------|-------------------|
| `apply-mysql-tuning.sh` | Sets `innodb_buffer_pool_size` from **local** `MemTotal` (~45% of RAM). On a large laptop this can allocate **tens of GiB** to MySQL and cause system instability. |
| `apply.sh` | Also disables apt timers, sysctl, systemd limits — intended for the target server. |

### MySQL tuning

- **Production use (EC2):** `sudo CONFIRM_MYSQL_TUNING=yes ./provisioning/apply-mysql-tuning.sh`
- **Preview only (any machine, no write/restart):** `sudo MYSQL_TUNING_DRY_RUN=1 ./provisioning/apply-mysql-tuning.sh`
- **Verify after apply (read-only):** `./provisioning/verify-mysql-tuning.sh` — compares live pool to policy including InnoDB round-up.

Do **not** restart MySQL locally to “test” tuning logic; use `MYSQL_TUNING_DRY_RUN=1` and inspect the printed config.

## Full server apply

```bash
sudo CONFIRM_MYSQL_TUNING=yes ./provisioning/apply.sh
```

`apply.sh` sources `apply-mysql-tuning.sh`; the same confirmation is required before MySQL is modified.
