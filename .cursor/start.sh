#!/usr/bin/env bash
# Per-boot startup for the xprem Cloud Agent dev environment.
#
# Brings up the PostgreSQL and ClickHouse services the control plane connects
# to, creates the dev/test databases if missing, and returns once both are
# reachable. Idempotent: safe to run on every boot. The API and dashboard
# processes themselves are launched by the environment.json terminals.
set -euo pipefail

echo "==> [start] PostgreSQL"
# Discover the installed cluster instead of hardcoding a major version, so a
# different PostgreSQL package version still starts.
read -r PG_VER PG_CLUSTER < <(pg_lsclusters -h 2>/dev/null | awk 'NR==1 {print $1, $2}')
PG_VER="${PG_VER:-16}"
PG_CLUSTER="${PG_CLUSTER:-main}"
echo "    using cluster ${PG_VER}/${PG_CLUSTER}"
sudo pg_ctlcluster "$PG_VER" "$PG_CLUSTER" start 2>/dev/null \
  || sudo pg_ctlcluster "$PG_VER" "$PG_CLUSTER" restart 2>/dev/null \
  || true

echo "    waiting for PostgreSQL to accept connections"
for _ in $(seq 1 30); do
  if sudo -u postgres pg_isready -q >/dev/null 2>&1; then break; fi
  sleep 1
done
sudo -u postgres pg_isready || { echo "PostgreSQL did not become ready" >&2; exit 1; }

# Dev/test role + databases (idempotent).
sudo -u postgres psql -v ON_ERROR_STOP=1 -c "ALTER USER postgres WITH PASSWORD 'postgres';" >/dev/null
sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='xprem_dev'"  | grep -q 1 || sudo -u postgres createdb xprem_dev
sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='xprem_test'" | grep -q 1 || sudo -u postgres createdb xprem_test

echo "==> [start] ClickHouse"
sudo mkdir -p /var/run/clickhouse-server
sudo chown clickhouse:clickhouse /var/run/clickhouse-server 2>/dev/null || true
sudo clickhouse start >/dev/null 2>&1 || true

echo "    waiting for ClickHouse to accept connections"
for _ in $(seq 1 30); do
  if clickhouse-client --host 127.0.0.1 --query "SELECT 1" >/dev/null 2>&1; then break; fi
  sleep 1
done
clickhouse-client --host 127.0.0.1 --query "SELECT 1" >/dev/null 2>&1 || { echo "ClickHouse did not become ready" >&2; exit 1; }

clickhouse-client --host 127.0.0.1 --query "CREATE DATABASE IF NOT EXISTS xprem_dev"
clickhouse-client --host 127.0.0.1 --query "CREATE DATABASE IF NOT EXISTS xprem_test"

echo "==> [start] services ready (PostgreSQL :5432, ClickHouse :9000/:8123)"
