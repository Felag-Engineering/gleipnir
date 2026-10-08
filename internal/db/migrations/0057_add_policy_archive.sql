-- sqlc-only schema delta for 0057_add_policy_archive.go; never executed at
-- runtime. 0001_initial.sql cannot gain this column itself because the later
-- table-rebuild migrations (0021/0026/0032) copy policies with SELECT *, so a
-- ninth column would break them on a fresh database. The Go migration is
-- authoritative; this file only tells sqlc the column exists.
ALTER TABLE policies ADD COLUMN deleted_at TEXT;
