package setup

// GuestCapacityScript verifies guest-observed capacity after a resource update.
// Linux reserves some physical memory, so 4 GiB guests need not report 4096 MiB
// in MemTotal. Do not use MemAvailable: running agents legitimately consume RAM.
const GuestCapacityScript = `set -eu
meminfo="${FENNEC_PROC_MEMINFO:-/proc/meminfo}"
max_attempts="${FENNEC_CAPACITY_ATTEMPTS:-46}"
attempt=0
while :; do
 attempt=$((attempt + 1))
 memory=$(awk '/^MemTotal:/ { print $2; exit }' "$meminfo" 2>/dev/null || echo 0)
 if [ -n "${FENNEC_CPU_COUNT:-}" ]; then
  cpus="$FENNEC_CPU_COUNT"
 elif command -v getconf >/dev/null 2>&1; then
  cpus=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 0)
 elif command -v nproc >/dev/null 2>&1; then
  cpus=$(nproc 2>/dev/null || echo 0)
 else
  cpus=$(awk '/^processor[[:space:]]*:/ { n++ } END { print n+0 }' /proc/cpuinfo 2>/dev/null || echo 0)
 fi
 case "$memory:$cpus" in
  ''|*[!0-9:]*) memory=0; cpus=0 ;;
 esac
 if [ "$memory" -ge 3891200 ] && [ "$cpus" -ge 2 ]; then
  printf '%s\n' 'guest_capacity_verified'
  exit 0
 fi
 if [ "$attempt" -ge "$max_attempts" ]; then
  echo 'cloud resources have not reached 2 vCPUs and 4 GiB; resource reconciliation required' >&2
  exit 1
 fi
 sleep 2
done
`
