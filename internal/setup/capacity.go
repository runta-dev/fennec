package setup

// GuestCapacityScript verifies guest-observed capacity after a resource update.
// Linux reserves some physical memory, so 4 GiB guests need not report 4096 MiB
// in MemTotal. Do not use MemAvailable: running agents legitimately consume RAM.
const GuestCapacityScript = `python3 - <<'CAPACITY'
import os,time
end=time.monotonic()+90
while True:
 with open('/proc/meminfo') as f:
  memory=next(int(line.split()[1]) for line in f if line.startswith('MemTotal:'))
 cpus=os.cpu_count() or 0
 if memory >= 3800*1024 and cpus >= 2:
  print('guest_capacity_verified')
  break
 if time.monotonic()>=end:
  raise SystemExit('cloud resources have not reached 2 vCPUs and 4 GiB; resource reconciliation required')
 time.sleep(2)
CAPACITY
`
