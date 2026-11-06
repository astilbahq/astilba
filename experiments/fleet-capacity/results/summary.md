| Run | Agents | q/s | q/s per agent | DB CPU cores | App CPU cores | DB anon+shm MiB | App anon MiB | Clients max / mean active | Agent B/s in / out per agent | DB B/s in / out per agent | WAL KiB/s | Logs B/s per agent | Heartbeat p50 / p99 ms | Non-2xx | pgbench -S TPS | Host load |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- | --- | ---: | ---: | --- | ---: | ---: | --- |
| idle-0000 | 0 | 4 | NaN | 0.005 | 0.007 | 246 | 18 | 5 / 0.00 | NaN / NaN | NaN / NaN | 1 | NaN | 0.0 / 0.0 | 0 | 17374 | 16.56 30.06 25.33 |
| idle-0100 | 100 | 352 | 3.52 | 0.058 | 0.070 | 249 | 39 | 5 / 0.41 | 116 / 120 | 793 / 397 | 10 | 14 | 4.1 / 2097.2 | 33 | 6117 | 40.14 42.74 34.78 |
| idle-0500 | 500 | 1710 | 3.42 | 0.134 | 0.148 | 404 | 85 | 5 / 0.09 | 117 / 121 | 751 / 375 | 50 | 15 | 2.0 / 262.1 | 0 | 8690 | 8.70 14.93 22.66 |
| idle-1000 | 1000 | 3392 | 3.39 | 0.204 | 0.222 | 702 | 139 | 5 / 0.34 | 115 / 120 | 741 / 370 | 69 | 14 | 4.1 / 2097.2 | 0 | 17251 | 6.00 8.08 13.67 |
| idle-2000 | 2000 | 6767 | 3.38 | 0.325 | 0.373 | 1108 | 248 | 5 / 0.31 | 116 / 121 | 740 / 369 | 153 | 15 | 4.1 / 1048.6 | 109 | 15972 | 23.57 14.95 12.95 |
| sized-0100 | 100 | 357 | 3.57 | 0.020 | 0.023 | 544 | 41 | 12 / 0.00 | 117 / 120 | 815 / 403 | 11 | 15 | 4.1 / 8.2 | 0 | 26061 | 3.73 3.31 3.75 |
| sized-1000 | 1000 | 3436 | 3.44 | 0.120 | 0.100 | 2411 | 137 | 13 / 0.16 | 117 / 121 | 761 / 375 | 75 | 15 | 4.1 / 8.2 | 0 | 17244 | 2.10 4.02 4.69 |
| sized-2000 | 2000 | 6840 | 3.42 | 0.290 | 0.218 | 3428 | 257 | 13 / 0.56 | 117 / 121 | 750 / 373 | 147 | 15 | 4.1 / 65.5 | 0 | 14726 | 3.12 4.47 5.14 |
| tmpfs-active-1000-pool12 | 1000 | 5007 | 5.01 | 0.863 | 0.379 | 4643 | 144 | 13 / 1.22 | 220 / 731 | 1071 / 1208 | 342 | 34 | 4.1 / 524.3 | 0 | 14794 | 9.12 13.49 15.50 |
| tmpfs-active-1000 | 1000 | 4815 | 4.82 | 0.611 | 0.263 | 4579 | 134 | 5 / 0.97 | 209 / 652 | 1031 / 1105 | 312 | 32 | 8.2 / 524.3 | 0 | 24327 | 6.45 4.97 5.13 |
| tmpfs-idle-0100 | 100 | 355 | 3.55 | 0.031 | 0.038 | 508 | 38 | 5 / 0.06 | 116 / 119 | 797 / 399 | 10 | 15 | 4.1 / 16.4 | 0 | 20567 | 7.45 7.93 8.65 |
| tmpfs-idle-0300-rtt3-pool04 | 300 | 1005 | 3.35 | 0.078 | 0.093 | 771 | 62 | 5 / 0.16 | 114 / 118 | 734 / 369 | 26 | 14 | 16.4 / 1048.6 | 16 | 22360 | 4.53 4.59 3.81 |
| tmpfs-idle-0500 | 500 | 1724 | 3.45 | 0.071 | 0.086 | 1196 | 85 | 5 / 0.25 | 116 / 120 | 756 / 378 | 41 | 15 | 4.1 / 16.4 | 0 | 23903 | 3.21 4.88 7.04 |
| tmpfs-idle-0600-rtt3-pool04 | 600 | 1255 | 2.09 | 0.131 | 0.158 | 1407 | 92 | 5 / 0.25 | 108 / 115 | 475 / 231 | 57 | 14 | 2097.2 / 2097.2 | 2384 | 24743 | 4.40 4.71 4.79 |
| tmpfs-idle-0600-rtt3-pool12 | 600 | 2014 | 3.36 | 0.122 | 0.131 | 1455 | 95 | 13 / 0.06 | 114 / 117 | 741 / 367 | 49 | 14 | 8.2 / 16.4 | 0 | 23361 | 5.20 3.64 3.91 |
| tmpfs-idle-1000 | 1000 | 3351 | 3.35 | 0.149 | 0.185 | 2364 | 134 | 5 / 0.31 | 114 / 117 | 733 / 366 | 73 | 14 | 4.1 / 16.4 | 0 | 23656 | 3.87 5.17 5.47 |
| tmpfs-idle-1100-rtt3-pool12 | 1100 | 3669 | 3.34 | 0.168 | 0.173 | 2524 | 144 | 13 / 0.41 | 114 / 119 | 730 / 364 | 86 | 14 | 16.4 / 262.1 | 0 | 23409 | 9.23 5.60 4.51 |
| tmpfs-idle-1500-repeat | 1500 | 5014 | 3.34 | 0.177 | 0.211 | 2985 | 183 | 5 / 0.62 | 114 / 118 | 731 / 365 | 99 | 14 | 4.1 / 131.1 | 0 | 23618 | 5.24 6.18 6.57 |
| tmpfs-idle-1500-rtt3-pool12 | 1500 | 3877 | 2.58 | 0.232 | 0.231 | 2998 | 191 | 13 / 0.38 | 109 / 116 | 575 / 283 | 106 | 14 | 1048.6 / 2097.2 | 3368 | 19149 | 14.50 7.81 5.84 |
| tmpfs-idle-1500-rtt3-pool24 | 1500 | 5008 | 3.34 | 0.317 | 0.279 | 3089 | 194 | 25 / 0.34 | 114 / 117 | 733 / 364 | 109 | 14 | 8.2 / 65.5 | 0 | 19690 | 9.85 8.77 7.42 |
| tmpfs-idle-1500-slow-db | 1500 | 4990 | 3.33 | 0.225 | 0.275 | 2984 | 185 | 5 / 0.59 | 114 / 119 | 728 / 363 | 98 | 14 | 8.2 / 524.3 | 0 | 20576 | 6.17 6.15 5.73 |
| tmpfs-idle-1500 | 1500 | 5127 | 3.42 | 0.175 | 0.216 | 2984 | 185 | 5 / 0.31 | 115 / 120 | 746 / 373 | 103 | 14 | 4.1 / 262.1 | 0 | 13874 | 3.98 5.98 6.32 |
| tmpfs-idle-2000-contended | 2000 | 6162 | 3.08 | 0.379 | 0.437 | 3371 | 269 | 5 / 0.97 | 114 / 122 | 677 / 336 | 150 | 14 | 262.1 / 4194.3 | 2604 | 9054 | 18.32 20.11 18.42 |
| tmpfs-idle-2000-no-tick | 2000 | 957 | 0.48 | 0.104 | 0.143 | 3378 | 243 | 5 / 0.34 | 114 / 117 | 152 / 55 | 158 | 14 | 4.1 / 16.4 | 0 | 24564 | 2.79 3.29 4.12 |
| tmpfs-idle-2000-pool12 | 2000 | 6811 | 3.41 | 0.372 | 0.426 | 3429 | 254 | 13 / 0.62 | 115 / 119 | 745 / 371 | 146 | 14 | 4.1 / 1048.6 | 0 | 18560 | 10.49 14.74 15.70 |
| tmpfs-idle-2000-pool48 | 2000 | 6691 | 3.35 | 0.471 | 0.404 | 3569 | 248 | 49 / 0.91 | 114 / 117 | 742 / 365 | 150 | 14 | 4.1 / 32.8 | 0 | 23749 | 5.04 5.33 5.76 |
| tmpfs-idle-2000 | 2000 | 6670 | 3.33 | 0.231 | 0.293 | 3381 | 241 | 5 / 0.59 | 114 / 118 | 729 / 364 | 148 | 14 | 4.1 / 262.1 | 0 | 22003 | 4.85 6.01 5.65 |

### idle-0000

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.427/0.692/5.034 ms; options map[agents:0 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | NaN | 0.107 | 0.1 | 41.8% | 336 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | NaN | 0.306 | 0.1 | 19.8% | 335 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | NaN | 0.206 | 0.0 | 13.6% | 283 |
| UPDATE river_leader SET expires_at = coalesce($1::timestampt… | 0.2 | NaN | 0.106 | 0.0 | 7.0% | 0 |
| WITH jobs_to_schedule AS ( SELECT id, unique_key, unique_sta… | 0.2 | NaN | 0.088 | 0.0 | 5.8% | 0 |
| SELECT workspace_id FROM workspace_networks ORDER BY checked… | 0.2 | NaN | 0.043 | 0.0 | 2.8% | 0 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.0 | NaN | 0.171 | 0.0 | 1.8% | 3 |
| UPDATE alert_deliveries SET state='leased',attempts=attempts… | 0.1 | NaN | 0.041 | 0.0 | 1.3% | 0 |
| UPDATE alert_deliveries SET state='failed' WHERE id IN (SELE… | 0.1 | NaN | 0.025 | 0.0 | 0.8% | 0 |
| WITH topic_to_notify AS ( SELECT concat(coalesce($1::text, c… | 0.2 | NaN | 0.012 | 0.0 | 0.8% | 0 |
| SELECT w.id FROM workspaces w WHERE EXISTS(SELECT 1 FROM tro… | 0.0 | NaN | 0.074 | 0.0 | 0.8% | 0 |
| DELETE FROM river_job WHERE id IN ( SELECT id FROM river_job… | 0.0 | NaN | 0.045 | 0.0 | 0.5% | 0 |
| SELECT id FROM preview_policies WHERE enabled AND next_disco… | 0.0 | NaN | 0.042 | 0.0 | 0.4% | 0 |
| SELECT id, args, attempt, attempted_at, attempted_by, create… | 0.0 | NaN | 0.040 | 0.0 | 0.4% | 0 |
| SELECT id FROM preview_requests WHERE state<>$1 AND next_che… | 0.0 | NaN | 0.037 | 0.0 | 0.4% | 0 |

WAL fsync: 1/s, mean 1.95 ms. Database: 12129983 → 12220095 bytes (188 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.000 cores. Load 16.56 30.06 25.33 → 30.52 32.03 28.32.

Largest table growth: river_job +57344 (now 286720); river_leader +32768 (now 81920); execution_operations +0 (now 114688); accounts +0 (now 49152); sessions +0 (now 49152); audit_events +0 (now 49152); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 0.

### idle-0100

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.376/1.713/30.058 ms; options map[agents:100 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 113.0 | 1.130 | 0.047 | 5.3 | 38.7% | 347 |
| ListTroubleshootingOffers | 100.5 | 1.005 | 0.025 | 2.5 | 18.5% | 0 |
| poll lease release | 4.0 | 0.040 | 0.490 | 1.9 | 14.1% | 613 |
| poll command | 100.5 | 1.005 | 0.014 | 1.4 | 10.2% | 0 |
| RecordAgentHeartbeat | 6.8 | 0.068 | 0.122 | 0.8 | 6.0% | 1330 |
| poll lease take | 4.1 | 0.041 | 0.112 | 0.5 | 3.3% | 648 |
| lockAgentCredential | 1.7 | 0.017 | 0.155 | 0.3 | 1.9% | 209 |
| metrics insert | 1.7 | 0.017 | 0.151 | 0.3 | 1.9% | 3395 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.012 | 0.099 | 0.1 | 0.9% | 280 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.6 | 0.016 | 0.073 | 0.1 | 0.8% | 17 |
| metrics prune | 1.7 | 0.017 | 0.044 | 0.1 | 0.5% | 607 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.2 | 0.032 | 0.021 | 0.1 | 0.5% | 0 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.002 | 0.321 | 0.1 | 0.5% | 306 |
| SELECT kind,resource_id,environment_id FROM operational_aler… | 1.6 | 0.016 | 0.027 | 0.0 | 0.3% | 0 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.002 | 0.209 | 0.0 | 0.3% | 397 |

WAL fsync: 17/s, mean 6.16 ms. Database: 75830975 → 76224191 bytes (819 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.008 cores. Load 40.14 42.74 34.78 → 13.54 22.08 27.80.

Largest table growth: server_metrics +294912 (now 63545344); river_job +49152 (now 303104); river_leader +32768 (now 81920); servers +0 (now 270336); execution_operations +0 (now 114688); audit_events +0 (now 106496); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 0.

### idle-0500

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.396/0.489/1.745 ms; options map[agents:500 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 567.9 | 1.136 | 0.024 | 13.7 | 45.3% | 2000 |
| ListTroubleshootingOffers | 504.7 | 1.009 | 0.012 | 6.0 | 19.7% | 0 |
| poll command | 504.7 | 1.009 | 0.007 | 3.7 | 12.3% | 0 |
| RecordAgentHeartbeat | 34.4 | 0.069 | 0.070 | 2.4 | 7.9% | 6308 |
| UPDATE servers SET poll_id=$3,poll_until=$4 WHERE id=$1 AND … | 20.3 | 0.041 | 0.071 | 1.4 | 4.8% | 3876 |
| poll lease take | 20.3 | 0.041 | 0.064 | 1.3 | 4.3% | 3255 |
| metrics insert | 8.5 | 0.017 | 0.094 | 0.8 | 2.6% | 15437 |
| lockAgentCredential | 8.5 | 0.017 | 0.032 | 0.3 | 0.9% | 1126 |
| metrics prune | 8.5 | 0.017 | 0.029 | 0.2 | 0.8% | 11654 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.002 | 0.058 | 0.1 | 0.2% | 298 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.6 | 0.003 | 0.039 | 0.1 | 0.2% | 791 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.193 | 0.0 | 0.1% | 274 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.175 | 0.0 | 0.1% | 376 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.007 | 0.010 | 0.0 | 0.1% | 0 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.001 | 0.073 | 0.0 | 0.1% | 77 |

WAL fsync: 76/s, mean 0.39 ms. Database: 333043391 → 334583487 bytes (3208 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.020 cores. Load 8.70 14.93 22.66 → 6.16 9.74 16.64.

Largest table growth: server_metrics +1458176 (now 320462848); river_job +49152 (now 294912); servers +0 (now 1466368); audit_events +0 (now 221184); auth_attempts +0 (now 172032); execution_operations +0 (now 114688); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 68.
Oldest kept error: poll: Get "http://fleet-app-96dcb18f:8080/agent/v1/poll": net/http: timeout awaiting response headers

### idle-1000

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.365/0.479/2.745 ms; options map[agents:1000 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 1131.1 | 1.131 | 0.018 | 20.2 | 42.3% | 3200 |
| ListTroubleshootingOffers | 1006.8 | 1.007 | 0.009 | 9.0 | 18.8% | 0 |
| poll command | 1006.8 | 1.007 | 0.006 | 5.7 | 12.0% | 0 |
| RecordAgentHeartbeat | 66.7 | 0.067 | 0.051 | 3.4 | 7.1% | 10090 |
| metrics prune | 16.9 | 0.017 | 0.162 | 2.7 | 5.7% | 7643 |
| poll lease release | 40.7 | 0.041 | 0.060 | 2.4 | 5.1% | 8809 |
| poll lease take | 40.7 | 0.041 | 0.056 | 2.3 | 4.8% | 6887 |
| metrics insert | 16.9 | 0.017 | 0.078 | 1.3 | 2.7% | 20744 |
| lockAgentCredential | 16.9 | 0.017 | 0.022 | 0.4 | 0.8% | 1700 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.054 | 0.1 | 0.1% | 286 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.002 | 0.030 | 0.0 | 0.1% | 639 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.173 | 0.0 | 0.1% | 360 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.083 | 0.0 | 0.1% | 99 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.003 | 0.007 | 0.0 | 0.0% | 0 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.110 | 0.0 | 0.0% | 346 |

WAL fsync: 155/s, mean 0.26 ms. Database: 642586303 → 645699263 bytes (6484 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.030 cores. Load 6.00 8.08 13.67 → 11.55 8.93 11.46.

Largest table growth: server_metrics +2981888 (now 628965376); river_job +57344 (now 303104); workspaces +8192 (now 98304); servers +0 (now 3579904); audit_events +0 (now 368640); auth_attempts +0 (now 262144); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: poll: Get "http://fleet-app-2327772c:8080/agent/v1/poll": net/http: timeout awaiting response headers

Application warnings:

```
1 time=2026-11-05T15:18:47.532Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T15:18:45.477Z level=ERROR msg="leadership.Elector: Error attempting reelection" attempt=1 client_id=eb4012b02e6a_2026_11_05T15_16_02_738625 err="leadership.attemptReelect timed out after 5s: context deadline exceeded" sleep_
```

### idle-2000

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.351/0.486/2.292 ms; options map[agents:2000 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 2259.5 | 1.130 | 0.014 | 31.7 | 42.5% | 6542 |
| ListTroubleshootingOffers | 2008.9 | 1.004 | 0.007 | 15.0 | 20.2% | 0 |
| poll command | 2009.6 | 1.005 | 0.004 | 8.7 | 11.6% | 0 |
| RecordAgentHeartbeat | 135.8 | 0.068 | 0.052 | 7.1 | 9.5% | 22182 |
| poll lease release | 80.4 | 0.040 | 0.051 | 4.1 | 5.5% | 13792 |
| poll lease take | 80.4 | 0.040 | 0.050 | 4.0 | 5.4% | 12769 |
| metrics insert | 35.4 | 0.018 | 0.066 | 2.3 | 3.1% | 45681 |
| metrics prune | 35.4 | 0.018 | 0.020 | 0.7 | 0.9% | 28721 |
| lockAgentCredential | 35.4 | 0.018 | 0.018 | 0.6 | 0.8% | 6340 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.045 | 0.1 | 0.1% | 365 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.029 | 0.0 | 0.1% | 34 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.107 | 0.0 | 0.0% | 151 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.162 | 0.0 | 0.0% | 268 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.106 | 0.0 | 0.0% | 301 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.002 | 0.006 | 0.0 | 0.0% | 0 |

WAL fsync: 324/s, mean 0.27 ms. Database: 1278138047 → 1284396735 bytes (13035 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.061 cores. Load 23.57 14.95 12.95 → 7.74 8.54 10.29.

Largest table growth: server_metrics +6070272 (now 1257922560); river_job +81920 (now 319488); river_leader +32768 (now 81920); servers +0 (now 12738560); audit_events +0 (now 655360); auth_attempts +0 (now 450560); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: poll: Get "http://fleet-app-a58aa5f5:8080/agent/v1/poll": net/http: timeout awaiting response headers

Application warnings:

```
4 time=2026-11-05T15:32:55.161Z level=ERROR msg="maintenance.PeriodicJobEnqueuer: Error starting transaction" error="context canceled"
      1 time=2026-11-05T15:33:06.031Z level=ERROR msg="leadership.Elector: Error attempting to elect" attempt=2 client_id=be930a367581_2026_11_05T15_29_39_749973 err="leadership.attemptElect timed out after 5s: error beginning transaction: context 
      1 time=2026-11-05T15:33:00.422Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T15:33:00.161Z level=ERROR msg="leadership.Elector: Error attempting to elect" attempt=1 client_id=be930a367581_2026_11_05T15_29_39_749973 err="leadership.attemptElect timed out after 5s: error beginning transaction: context 
      1 time=2026-11-05T15:32:51.303Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=3 client_id=be930a367581_2026_11_05T15_29_39_749973 err="leadership.Elector.attemptResign timed out after 3s: context deadline exceeded" 
      1 time=2026-11-05T15:32:50.575Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T15:32:46.385Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=2 client_id=be930a367581_2026_11_05T15_29_39_749973 err="leadership.Elector.attemptResign timed out after 2s: context deadline exceeded" 
      1 time=2026-11-05T15:32:43.370Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=1 client_id=be930a367581_2026_11_05T15_29_39_749973 err="leadership.Elector.attemptResign timed out after 1s: context deadline exceeded" 
      1 time=2026-11-05T15:32:42.369Z level=WARN msg="leadership.Elector: Current leader stepping down because the reelection deadline elapsed after an error" client_id=be930a367581_2026_11_05T15_29_39_749973
      1 time=2026-11-05T15:32:40.729Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T15:32:38.526Z level=ERROR msg="leadership.Elector: Error attempting reelection" attempt=1 client_id=be930a367581_2026_11_05T15_29_39_749973 err="leadership.attemptReelect timed out after 5s: context deadline exceeded" sleep_
```

### sized-0100

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.323/0.483/0.554 ms; options map[agents:100 appCPUs:0.1 appCpuset:0-11 appMemory: dbCPUs:0.8 dbCpuset:0-11 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 114.7 | 1.147 | 0.017 | 1.9 | 42.9% | 505 |
| ListTroubleshootingOffers | 102.1 | 1.021 | 0.007 | 0.8 | 17.1% | 0 |
| poll command | 102.1 | 1.021 | 0.005 | 0.5 | 11.5% | 0 |
| RecordAgentHeartbeat | 6.8 | 0.068 | 0.057 | 0.4 | 8.8% | 1268 |
| poll lease release | 4.1 | 0.041 | 0.060 | 0.2 | 5.5% | 747 |
| poll lease take | 4.1 | 0.041 | 0.054 | 0.2 | 4.9% | 633 |
| metrics insert | 1.7 | 0.017 | 0.073 | 0.1 | 2.8% | 3439 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.012 | 0.048 | 0.1 | 1.3% | 351 |
| metrics prune | 1.7 | 0.017 | 0.026 | 0.0 | 1.0% | 1138 |
| lockAgentCredential | 1.7 | 0.017 | 0.018 | 0.0 | 0.7% | 123 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.002 | 0.142 | 0.0 | 0.6% | 259 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.6 | 0.016 | 0.018 | 0.0 | 0.6% | 17 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.002 | 0.101 | 0.0 | 0.5% | 354 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.2 | 0.032 | 0.005 | 0.0 | 0.4% | 0 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.003 | 0.037 | 0.0 | 0.3% | 50 |

WAL fsync: 17/s, mean 0.00 ms. Database: 75757247 → 76232383 bytes (990 bytes/s). Throttled µs: DB 0, app 1339536. Generator CPU 0.004 cores. Load 3.73 3.31 3.75 → 1.82 3.21 3.62.

Largest table growth: server_metrics +303104 (now 63520768); river_job +73728 (now 311296); river_leader +32768 (now 81920); servers +8192 (now 221184); audit_events +0 (now 139264); execution_operations +0 (now 114688); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 0.

### sized-1000

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.353/0.490/0.564 ms; options map[agents:1000 appCPUs:0.4 appCpuset:0-11 appMemory: dbCPUs:0.8 dbCpuset:0-11 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 1145.7 | 1.146 | 0.010 | 11.1 | 43.8% | 3407 |
| ListTroubleshootingOffers | 1018.9 | 1.019 | 0.005 | 4.9 | 19.3% | 0 |
| poll command | 1018.9 | 1.019 | 0.003 | 2.9 | 11.5% | 0 |
| RecordAgentHeartbeat | 68.4 | 0.068 | 0.032 | 2.2 | 8.7% | 12382 |
| poll lease release | 41.3 | 0.041 | 0.034 | 1.4 | 5.6% | 7213 |
| poll lease take | 41.2 | 0.041 | 0.032 | 1.3 | 5.2% | 6561 |
| metrics insert | 17.1 | 0.017 | 0.050 | 0.9 | 3.4% | 21082 |
| metrics prune | 17.1 | 0.017 | 0.013 | 0.2 | 0.9% | 12320 |
| lockAgentCredential | 17.1 | 0.017 | 0.012 | 0.2 | 0.8% | 1650 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.030 | 0.0 | 0.1% | 265 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.9 | 0.002 | 0.016 | 0.0 | 0.1% | 0 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 1.399 | 0.0 | 0.1% | 1334 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.126 | 0.0 | 0.1% | 254 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.4 | 0.000 | 0.053 | 0.0 | 0.1% | 101 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.085 | 0.0 | 0.1% | 341 |

WAL fsync: 75/s, mean 0.00 ms. Database: 642963135 → 646067903 bytes (6467 bytes/s). Throttled µs: DB 0, app 111253. Generator CPU 0.018 cores. Load 2.10 4.02 4.69 → 2.73 3.10 3.94.

Largest table growth: server_metrics +2981888 (now 629202944); river_job +49152 (now 286720); workspaces +8192 (now 122880); servers +0 (now 3629056); audit_events +0 (now 385024); auth_attempts +0 (now 286720); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: poll: Get "http://fleet-app-4f5b9233:8080/agent/v1/poll": net/http: timeout awaiting response headers

Application warnings:

```
1 time=2026-11-05T19:24:13.584Z level=ERROR msg="leadership.Elector: Error attempting reelection" attempt=1 client_id=ff96b9e8e37e_2026_11_05T19_21_32_497030 err="leadership.attemptReelect timed out after 5s: context deadline exceeded" sleep_
```

### sized-2000

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.363/0.491/0.540 ms; options map[agents:2000 appCPUs:0.8 appCpuset:0-11 appMemory: dbCPUs:0.8 dbCpuset:0-11 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 2286.5 | 1.143 | 0.012 | 28.2 | 42.6% | 6249 |
| ListTroubleshootingOffers | 2033.2 | 1.017 | 0.006 | 12.7 | 19.1% | 0 |
| poll command | 2033.2 | 1.017 | 0.004 | 7.4 | 11.2% | 0 |
| RecordAgentHeartbeat | 137.3 | 0.069 | 0.037 | 5.1 | 7.8% | 26241 |
| poll lease release | 81.8 | 0.041 | 0.042 | 3.5 | 5.3% | 12608 |
| metrics prune | 34.2 | 0.017 | 0.097 | 3.3 | 5.0% | 22379 |
| poll lease take | 81.8 | 0.041 | 0.039 | 3.2 | 4.8% | 13061 |
| metrics insert | 34.2 | 0.017 | 0.055 | 1.9 | 2.9% | 41298 |
| lockAgentCredential | 34.2 | 0.017 | 0.015 | 0.5 | 0.8% | 4048 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.041 | 0.0 | 0.1% | 287 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 2.700 | 0.0 | 0.1% | 2486 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.025 | 0.0 | 0.1% | 0 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.104 | 0.0 | 0.1% | 126 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.143 | 0.0 | 0.0% | 264 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.096 | 0.0 | 0.0% | 327 |

WAL fsync: 116/s, mean 0.00 ms. Database: 1281562303 → 1287689919 bytes (12763 bytes/s). Throttled µs: DB 1954474, app 383929. Generator CPU 0.040 cores. Load 3.12 4.47 5.14 → 8.26 5.67 5.28.

Largest table growth: server_metrics +5939200 (now 1258143744); river_job +98304 (now 344064); workspaces +8192 (now 163840); servers +0 (now 15589376); audit_events +0 (now 671744); auth_attempts +0 (now 499712); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: poll: Get "http://fleet-app-cdea799e:8080/agent/v1/poll": net/http: timeout awaiting response headers

Application warnings:

```
1 time=2026-11-05T19:10:44.364Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=3 client_id=1d84936f3eed_2026_11_05T19_07_21_266867 err="leadership.Elector.attemptResign timed out after 3s: context deadline exceeded" 
      1 time=2026-11-05T19:10:41.824Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T19:10:39.476Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=2 client_id=1d84936f3eed_2026_11_05T19_07_21_266867 err="leadership.Elector.attemptResign timed out after 2s: context deadline exceeded" 
      1 time=2026-11-05T19:10:36.436Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=1 client_id=1d84936f3eed_2026_11_05T19_07_21_266867 err="leadership.Elector.attemptResign timed out after 1s: context deadline exceeded" 
      1 time=2026-11-05T19:10:35.435Z level=WARN msg="leadership.Elector: Current leader stepping down because the reelection deadline elapsed after an error" client_id=1d84936f3eed_2026_11_05T19_07_21_266867
      1 time=2026-11-05T19:10:32.072Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T19:10:31.455Z level=ERROR msg="leadership.Elector: Error attempting reelection" attempt=1 client_id=1d84936f3eed_2026_11_05T19_07_21_266867 err="leadership.attemptReelect timed out after 5s: context deadline exceeded" sleep_
```

### tmpfs-active-1000-pool12

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.369/0.460/0.558 ms; options map[agents:1000 appCPUs: appCpuset:0-11 appMemory: dbCPUs: dbCpuset:0-11 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:24 perWorkspace:5 services:5 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| poll command | 1127.3 | 1.127 | 0.268 | 301.7 | 78.9% | 125 |
| AuthenticateAgent | 1420.0 | 1.420 | 0.019 | 26.8 | 7.0% | 5626 |
| poll lease take | 187.4 | 0.187 | 0.064 | 12.0 | 3.1% | 72770 |
| poll lease release | 187.3 | 0.187 | 0.061 | 11.5 | 3.0% | 57414 |
| ListTroubleshootingOffers | 1127.3 | 1.127 | 0.010 | 11.3 | 3.0% | 0 |
| RecordAgentHeartbeat | 68.7 | 0.069 | 0.058 | 4.0 | 1.0% | 27687 |
| SELECT workspace_id FROM servers WHERE id=$1 | 148.3 | 0.148 | 0.015 | 2.3 | 0.6% | 97 |
| metrics insert | 17.0 | 0.017 | 0.094 | 1.6 | 0.4% | 39003 |
| metrics ownership | 85.2 | 0.085 | 0.016 | 1.4 | 0.4% | 62 |
| metrics prune | 17.0 | 0.017 | 0.064 | 1.1 | 0.3% | 9449 |
| lockAgentCredential | 36.6 | 0.037 | 0.029 | 1.1 | 0.3% | 2636 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 5.3 | 0.005 | 0.180 | 0.9 | 0.2% | 19803 |
| SELECT command,report_sequence,state FROM execution_operatio… | 19.6 | 0.020 | 0.045 | 0.9 | 0.2% | 1369 |
| SELECT id FROM workspaces WHERE id=$1 FOR UPDATE | 17.8 | 0.018 | 0.038 | 0.7 | 0.2% | 1046 |
| SELECT state,command,cancel_requested FROM execution_operati… | 17.4 | 0.017 | 0.038 | 0.7 | 0.2% | 1191 |

WAL fsync: 194/s, mean 0.00 ms. Database: 2527925951 → 2531636927 bytes (7729 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.073 cores. Load 9.12 13.49 15.50 → 5.79 10.87 13.74.

Largest table growth: server_metrics +13156352 (now 2483912704); execution_operations +827392 (now 8822784); visual_plans +319488 (now 3088384); specification_revisions +253952 (now 2859008); visual_requests +237568 (now 2580480); visual_runtime +122880 (now 1916928); 

Deploys in window: started 138, finished 169, failed 0; delivery p50/p99 ms 1049/2097. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: poll: Get "http://fleet-app-a107d374:8080/agent/v1/poll": net/http: timeout awaiting response headers

Application warnings:

```
1 time=2026-11-05T17:38:31.809Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=2 client_id=d9b23e0bd1eb_2026_11_05T17_26_48_156477 err="leadership.Elector.attemptResign timed out after 2s: context deadline exceeded" 
      1 time=2026-11-05T17:38:29.019Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=1 client_id=d9b23e0bd1eb_2026_11_05T17_26_48_156477 err="leadership.Elector.attemptResign timed out after 1s: context deadline exceeded" 
      1 time=2026-11-05T17:38:28.018Z level=WARN msg="leadership.Elector: Current leader stepping down because the reelection deadline elapsed after an error" client_id=d9b23e0bd1eb_2026_11_05T17_26_48_156477
      1 time=2026-11-05T17:38:24.033Z level=ERROR msg="leadership.Elector: Error attempting reelection" attempt=1 client_id=d9b23e0bd1eb_2026_11_05T17_26_48_156477 err="leadership.attemptReelect timed out after 5s: context deadline exceeded" sleep_
```

### tmpfs-active-1000

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.329/0.412/0.487 ms; options map[agents:1000 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:24 perWorkspace:5 services:5 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| poll command | 1110.2 | 1.110 | 0.205 | 228.0 | 81.2% | 123 |
| AuthenticateAgent | 1381.0 | 1.381 | 0.012 | 16.4 | 5.8% | 5568 |
| ListTroubleshootingOffers | 1110.2 | 1.110 | 0.007 | 7.3 | 2.6% | 0 |
| poll lease take | 167.8 | 0.168 | 0.042 | 7.1 | 2.5% | 64759 |
| poll lease release | 167.8 | 0.168 | 0.041 | 6.8 | 2.4% | 51983 |
| metrics prune | 17.1 | 0.017 | 0.187 | 3.2 | 1.1% | 18062 |
| RecordAgentHeartbeat | 68.1 | 0.068 | 0.042 | 2.9 | 1.0% | 23307 |
| SELECT workspace_id FROM servers WHERE id=$1 | 128.8 | 0.129 | 0.009 | 1.2 | 0.4% | 119 |
| metrics insert | 17.1 | 0.017 | 0.070 | 1.2 | 0.4% | 38788 |
| metrics ownership | 85.3 | 0.085 | 0.012 | 1.0 | 0.4% | 80 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 4.9 | 0.005 | 0.138 | 0.7 | 0.2% | 18177 |
| lockAgentCredential | 34.8 | 0.035 | 0.018 | 0.6 | 0.2% | 2739 |
| SELECT command,report_sequence,state FROM execution_operatio… | 17.7 | 0.018 | 0.032 | 0.6 | 0.2% | 1126 |
| SELECT state,command,cancel_requested FROM execution_operati… | 15.7 | 0.016 | 0.028 | 0.4 | 0.2% | 1241 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 2.9 | 0.003 | 0.148 | 0.4 | 0.2% | 25314 |

WAL fsync: 287/s, mean 0.00 ms. Database: 2514966207 → 2528892607 bytes (29005 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.055 cores. Load 6.45 4.97 5.13 → 5.75 6.08 5.76.

Largest table growth: server_metrics +13139968 (now 2482364416); execution_operations +712704 (now 8568832); visual_plans +311296 (now 2768896); specification_revisions +262144 (now 2654208); visual_requests +253952 (now 2277376); river_job +245760 (now 2801664); 

Deploys in window: started 136, finished 137, failed 0; delivery p50/p99 ms 1049/2097. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: poll: Get "http://fleet-app-7ee4d3d7:8080/agent/v1/poll": net/http: timeout awaiting response headers

### tmpfs-idle-0100

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.372/0.432/0.500 ms; options map[agents:100 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 114.0 | 1.140 | 0.028 | 3.2 | 45.3% | 612 |
| ListTroubleshootingOffers | 101.5 | 1.015 | 0.012 | 1.3 | 18.1% | 0 |
| poll command | 101.5 | 1.015 | 0.008 | 0.8 | 11.9% | 0 |
| RecordAgentHeartbeat | 6.8 | 0.068 | 0.074 | 0.5 | 7.2% | 1203 |
| poll lease release | 4.0 | 0.040 | 0.081 | 0.3 | 4.7% | 701 |
| poll lease take | 4.0 | 0.040 | 0.073 | 0.3 | 4.2% | 645 |
| metrics insert | 1.7 | 0.017 | 0.096 | 0.2 | 2.3% | 3373 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.012 | 0.058 | 0.1 | 1.0% | 310 |
| metrics prune | 1.7 | 0.017 | 0.029 | 0.0 | 0.7% | 630 |
| lockAgentCredential | 1.7 | 0.017 | 0.028 | 0.0 | 0.7% | 131 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.6 | 0.016 | 0.029 | 0.0 | 0.7% | 67 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.002 | 0.218 | 0.0 | 0.6% | 259 |
| UPDATE operational_alerts SET state=$6,reason=$4,resolved_at… | 3.2 | 0.032 | 0.010 | 0.0 | 0.4% | 0 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.002 | 0.123 | 0.0 | 0.4% | 356 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.003 | 0.070 | 0.0 | 0.3% | 50 |

WAL fsync: 17/s, mean 0.00 ms. Database: 75708095 → 76134079 bytes (887 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.005 cores. Load 7.45 7.93 8.65 → 5.01 7.83 8.76.

Largest table growth: server_metrics +294912 (now 63488000); river_job +57344 (now 294912); river_leader +32768 (now 81920); servers +8192 (now 229376); execution_operations +0 (now 114688); audit_events +0 (now 106496); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 0.

### tmpfs-idle-0300-rtt3-pool04

Window 480s; revision d1a9b46; ping round-trip min/avg/max = 2.967/3.574/3.919 ms; options map[agents:300 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:2800us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 poolSize:4 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 331.9 | 1.106 | 0.021 | 6.9 | 39.9% | 1415 |
| ListTroubleshootingOffers | 295.2 | 0.984 | 0.012 | 3.6 | 20.8% | 0 |
| poll command | 295.2 | 0.984 | 0.007 | 2.1 | 12.1% | 0 |
| RecordAgentHeartbeat | 20.0 | 0.067 | 0.078 | 1.6 | 9.0% | 3382 |
| poll lease release | 11.7 | 0.039 | 0.077 | 0.9 | 5.2% | 2161 |
| poll lease take | 11.7 | 0.039 | 0.075 | 0.9 | 5.1% | 1883 |
| metrics insert | 5.0 | 0.017 | 0.094 | 0.5 | 2.7% | 9346 |
| metrics prune | 5.0 | 0.017 | 0.065 | 0.3 | 1.9% | 2158 |
| lockAgentCredential | 5.0 | 0.017 | 0.028 | 0.1 | 0.8% | 490 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.001 | 0.464 | 0.1 | 0.6% | 263 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.004 | 0.056 | 0.1 | 0.4% | 312 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.6 | 0.005 | 0.032 | 0.1 | 0.3% | 404 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.011 | 0.009 | 0.0 | 0.2% | 0 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.001 | 0.141 | 0.0 | 0.2% | 390 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.001 | 0.062 | 0.0 | 0.1% | 67 |

WAL fsync: 48/s, mean 0.00 ms. Database: 202995391 → 203994815 bytes (2082 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.014 cores. Load 4.53 4.59 3.81 → 3.92 6.03 5.21.

Largest table growth: server_metrics +868352 (now 190791680); river_job +57344 (now 294912); river_leader +32768 (now 81920); servers +0 (now 581632); audit_events +0 (now 163840); auth_attempts +0 (now 131072); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 16.
Oldest kept error: 10:49:57 poll: Get "http://fleet-app-c63a4c19:8080/agent/v1/poll": net/http: timeout awaiting response headers

Pool log in the window:

```
2026/11/06 10:47:38 INFO database pool size=4 open=4 inUse=3 acquires=58817 waited=28570 waitSeconds=74.319057006 canceled=0
2026/11/06 10:48:38 INFO database pool size=4 open=4 inUse=4 acquires=58831 waited=31372 waitSeconds=89.267251521 canceled=0
2026/11/06 10:49:38 INFO database pool size=4 open=4 inUse=3 acquires=58876 waited=32980 waitSeconds=178.584479624 canceled=0
2026/11/06 10:50:38 INFO database pool size=4 open=4 inUse=3 acquires=57043 waited=42853 waitSeconds=5018.43435273 canceled=6
2026/11/06 10:51:38 INFO database pool size=4 open=4 inUse=3 acquires=57100 waited=36918 waitSeconds=3324.979645897 canceled=4
2026/11/06 10:52:38 INFO database pool size=4 open=4 inUse=3 acquires=58902 waited=27168 waitSeconds=59.519619043 canceled=0
2026/11/06 10:53:38 INFO database pool size=4 open=4 inUse=2 acquires=58880 waited=27587 waitSeconds=91.126487971 canceled=0
2026/11/06 10:54:38 INFO database pool size=4 open=4 inUse=4 acquires=58724 waited=28059 waitSeconds=67.026147428 canceled=0
```

Application warnings:

```
1 2026/11/06 10:50:57 ERROR request failed request_id=8586e39b-db03-4dcd-b1e4-7a168fa57654 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 10:50:57 ERROR request failed request_id=7b197a78-f54e-430e-83d4-efdd45d0b822 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 10:50:56 ERROR request failed request_id=7356ed09-56bc-434f-9e8d-049cb7fd41fb route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 10:50:56 ERROR request failed request_id=440a6813-8966-4061-aec4-1f9887477d40 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 10:50:03 ERROR request failed request_id=0cddb620-5613-4c46-85a7-dda248956078 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 10:50:02 ERROR request failed request_id=8e23d339-bd98-4f52-9e97-c2614dea7243 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 10:50:02 ERROR request failed request_id=6f9153a2-d3fe-4c43-b94b-6e8c27aa9cf7 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 10:50:01 ERROR request failed request_id=bf67cb78-e418-4198-b7d4-496b1f79cd99 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 10:50:01 ERROR request failed request_id=1d03feb3-36ab-425a-8d63-b8f8b7f81b8a route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 10:50:00 ERROR request failed request_id=7cc51467-f41f-4ca5-983f-c68b5b52b34a route=/agent/v1/poll error.category=context_canceled
```

### tmpfs-idle-0500

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.330/0.432/0.594 ms; options map[agents:500 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 572.6 | 1.145 | 0.012 | 6.8 | 42.2% | 2035 |
| ListTroubleshootingOffers | 509.8 | 1.020 | 0.006 | 3.0 | 18.5% | 0 |
| poll command | 509.8 | 1.020 | 0.004 | 1.9 | 11.6% | 0 |
| RecordAgentHeartbeat | 33.9 | 0.068 | 0.043 | 1.4 | 9.0% | 5713 |
| poll lease release | 20.3 | 0.041 | 0.046 | 0.9 | 5.8% | 3953 |
| poll lease take | 20.3 | 0.041 | 0.043 | 0.9 | 5.4% | 3342 |
| metrics insert | 8.5 | 0.017 | 0.059 | 0.5 | 3.2% | 15448 |
| metrics prune | 8.5 | 0.017 | 0.043 | 0.4 | 2.3% | 2831 |
| lockAgentCredential | 8.5 | 0.017 | 0.014 | 0.1 | 0.8% | 812 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.002 | 0.040 | 0.0 | 0.3% | 265 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.141 | 0.0 | 0.2% | 262 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.6 | 0.003 | 0.016 | 0.0 | 0.2% | 740 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.092 | 0.0 | 0.1% | 373 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.007 | 0.004 | 0.0 | 0.1% | 0 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.001 | 0.036 | 0.0 | 0.1% | 77 |

WAL fsync: 56/s, mean 0.00 ms. Database: 332445375 → 334083775 bytes (3410 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.012 cores. Load 3.21 4.88 7.04 → 6.20 4.13 5.62.

Largest table growth: server_metrics +1499136 (now 320348160); river_job +81920 (now 319488); river_leader +32768 (now 81920); servers +0 (now 1048576); audit_events +0 (now 229376); auth_attempts +0 (now 172032); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 0.

### tmpfs-idle-0600-rtt3-pool04

Window 480s; revision d1a9b46; ping round-trip min/avg/max = 2.901/3.522/3.782 ms; options map[agents:600 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:2800us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 poolSize:4 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 416.8 | 0.695 | 0.027 | 11.4 | 35.3% | 1891 |
| ListTroubleshootingOffers | 348.5 | 0.581 | 0.016 | 5.4 | 16.8% | 0 |
| RecordAgentHeartbeat | 37.2 | 0.062 | 0.111 | 4.1 | 12.7% | 6599 |
| poll command | 347.2 | 0.579 | 0.010 | 3.4 | 10.3% | 0 |
| poll lease take | 20.8 | 0.035 | 0.110 | 2.3 | 7.1% | 3895 |
| poll lease release | 20.8 | 0.035 | 0.110 | 2.3 | 7.1% | 4028 |
| metrics insert | 9.9 | 0.016 | 0.150 | 1.5 | 4.5% | 18739 |
| metrics prune | 9.9 | 0.016 | 0.109 | 1.1 | 3.3% | 13935 |
| lockAgentCredential | 9.9 | 0.016 | 0.040 | 0.4 | 1.2% | 1025 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.1 | 0.002 | 0.092 | 0.1 | 0.3% | 294 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.003 | 0.055 | 0.1 | 0.3% | 886 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.296 | 0.1 | 0.2% | 387 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.001 | 0.130 | 0.0 | 0.1% | 96 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.005 | 0.012 | 0.0 | 0.1% | 0 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.198 | 0.0 | 0.1% | 343 |

WAL fsync: 85/s, mean 0.00 ms. Database: 394073791 → 395998911 bytes (4010 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.040 cores. Load 4.40 4.71 4.79 → 2.99 3.49 4.11.

Largest table growth: server_metrics +1753088 (now 381771776); river_job +65536 (now 311296); river_leader +32768 (now 81920); servers +0 (now 1294336); audit_events +0 (now 262144); auth_attempts +0 (now 188416); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: 11:07:41 poll: Get "http://fleet-app-6e0b598d:8080/agent/v1/poll": net/http: timeout awaiting response headers

Pool log in the window:

```
2026/11/06 11:00:53 INFO database pool size=4 open=4 inUse=3 acquires=72803 waited=72803 waitSeconds=38190.437408872 canceled=164
2026/11/06 11:01:53 INFO database pool size=4 open=4 inUse=3 acquires=72095 waited=72095 waitSeconds=38405.286430083 canceled=183
2026/11/06 11:02:53 INFO database pool size=4 open=4 inUse=4 acquires=71619 waited=71619 waitSeconds=38409.484026734 canceled=247
2026/11/06 11:03:53 INFO database pool size=4 open=4 inUse=4 acquires=72479 waited=72479 waitSeconds=38170.137857416 canceled=177
2026/11/06 11:04:53 INFO database pool size=4 open=4 inUse=3 acquires=71520 waited=71520 waitSeconds=38163.425648735 canceled=244
2026/11/06 11:05:53 INFO database pool size=4 open=4 inUse=4 acquires=72014 waited=72014 waitSeconds=38337.432073522 canceled=253
2026/11/06 11:06:53 INFO database pool size=4 open=4 inUse=4 acquires=72683 waited=72683 waitSeconds=38361.358050122 canceled=144
2026/11/06 11:07:53 INFO database pool size=4 open=4 inUse=4 acquires=73042 waited=73042 waitSeconds=38175.336933146 canceled=176
```

Application warnings:

```
1 2026/11/06 11:08:27 ERROR request failed request_id=fb5a84f7-fccb-4e82-9e46-ddbd2c2a31b2 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:27 ERROR request failed request_id=9a5b1aac-9007-4dbe-b161-65ed894b2006 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:27 ERROR request failed request_id=806abdab-0117-4152-9ffa-57b795162a36 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:26 ERROR request failed request_id=9e2f5605-e914-408a-8125-517ee014ee69 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:26 ERROR request failed request_id=9041cb46-79d4-4895-86f0-dccee90f6ae3 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:26 ERROR request failed request_id=689b627b-2629-4340-959d-9a412febae94 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:26 ERROR request failed request_id=3adb53df-c007-4482-a540-e87ee09bc86b route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:26 ERROR request failed request_id=3419eaa4-b724-4035-9251-af74a434fa6b route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:25 ERROR request failed request_id=69f0a996-6e2a-4af1-84e2-5c43884829a7 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:25 ERROR request failed request_id=0c25370a-86fe-491e-ab82-482dc6e23516 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:24 ERROR request failed request_id=dc000749-717e-4f29-a2be-f212cb81aab5 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:24 ERROR request failed request_id=6c94ebb7-dad2-486a-a58c-5570aa785860 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:23 ERROR request failed request_id=f23901d2-cafd-41a3-b2a6-e51d1bb09073 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:23 ERROR request failed request_id=14cf35b0-3f1c-47c2-954e-c2ef4b7de155 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:22 ERROR request failed request_id=db340b32-07ce-41d0-a35a-4f3e630aa7ff route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:22 ERROR request failed request_id=cabf4a6e-7e2e-4eca-b9bd-4f25d5251960 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:22 ERROR request failed request_id=ae2e63a5-0698-43b4-b941-074a7ce8c691 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:22 ERROR request failed request_id=1ab54228-7f79-4c0e-85ae-6750c28f7889 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:21 ERROR request failed request_id=18aeacbb-48b2-4239-b5bc-385b8ab9e937 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:08:20 ERROR request failed request_id=d8fa368d-a3a6-48b4-8871-6e5562a67ef8 route=/agent/v1/poll error.category=context_canceled
```

### tmpfs-idle-0600-rtt3-pool12

Window 480s; revision d1a9b46; ping round-trip min/avg/max = 2.877/3.543/3.787 ms; options map[agents:600 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:2800us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 poolSize:12 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 669.8 | 1.116 | 0.016 | 10.5 | 40.0% | 1864 |
| ListTroubleshootingOffers | 595.9 | 0.993 | 0.008 | 4.9 | 18.9% | 0 |
| poll command | 595.9 | 0.993 | 0.006 | 3.3 | 12.5% | 0 |
| RecordAgentHeartbeat | 40.1 | 0.067 | 0.061 | 2.4 | 9.4% | 7899 |
| poll lease release | 23.8 | 0.040 | 0.065 | 1.6 | 5.9% | 3919 |
| poll lease take | 23.8 | 0.040 | 0.063 | 1.5 | 5.7% | 3770 |
| metrics insert | 10.0 | 0.017 | 0.091 | 0.9 | 3.5% | 18261 |
| metrics prune | 10.0 | 0.017 | 0.051 | 0.5 | 1.9% | 3809 |
| lockAgentCredential | 10.0 | 0.017 | 0.022 | 0.2 | 0.8% | 920 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.003 | 0.034 | 0.1 | 0.2% | 1043 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.002 | 0.045 | 0.1 | 0.2% | 270 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.159 | 0.0 | 0.1% | 259 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.001 | 0.081 | 0.0 | 0.1% | 95 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.006 | 0.007 | 0.0 | 0.1% | 0 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 1.335 | 0.0 | 0.1% | 1090 |

WAL fsync: 79/s, mean 0.00 ms. Database: 394327743 → 396261055 bytes (4027 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.021 cores. Load 5.20 3.64 3.91 → 5.92 4.20 3.93.

Largest table growth: server_metrics +1736704 (now 381730816); river_job +73728 (now 311296); river_leader +32768 (now 81920); servers +0 (now 1499136); audit_events +0 (now 262144); auth_attempts +0 (now 212992); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 0.

Pool log in the window:

```
2026/11/06 11:14:12 INFO database pool size=12 open=12 inUse=7 acquires=117621 waited=4366 waitSeconds=3.088332943 canceled=0
2026/11/06 11:15:12 INFO database pool size=12 open=12 inUse=4 acquires=117669 waited=4223 waitSeconds=2.544943067 canceled=0
2026/11/06 11:16:12 INFO database pool size=12 open=12 inUse=7 acquires=117753 waited=3011 waitSeconds=1.786793716 canceled=0
2026/11/06 11:17:12 INFO database pool size=12 open=12 inUse=11 acquires=117580 waited=3977 waitSeconds=3.485935166 canceled=0
2026/11/06 11:18:12 INFO database pool size=12 open=12 inUse=5 acquires=117556 waited=3423 waitSeconds=1.933815304 canceled=0
2026/11/06 11:19:12 INFO database pool size=12 open=12 inUse=8 acquires=117668 waited=3519 waitSeconds=2.207949333 canceled=0
2026/11/06 11:20:12 INFO database pool size=12 open=12 inUse=8 acquires=117596 waited=3827 waitSeconds=2.091287446 canceled=0
2026/11/06 11:21:12 INFO database pool size=12 open=12 inUse=6 acquires=117570 waited=3255 waitSeconds=1.733789833 canceled=0
```

### tmpfs-idle-1000

Window 480s; revision f06a5b8; ping round-trip min/avg/max = 0.344/0.447/0.566 ms; options map[agents:1000 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 1117.6 | 1.118 | 0.013 | 14.7 | 43.4% | 3813 |
| ListTroubleshootingOffers | 994.7 | 0.995 | 0.006 | 6.2 | 18.5% | 0 |
| poll command | 994.7 | 0.995 | 0.004 | 4.0 | 11.8% | 0 |
| RecordAgentHeartbeat | 66.7 | 0.067 | 0.047 | 3.1 | 9.2% | 10524 |
| UPDATE servers SET poll_id=$3,poll_until=$4 WHERE id=$1 AND … | 39.6 | 0.040 | 0.049 | 1.9 | 5.7% | 6850 |
| poll lease take | 39.6 | 0.040 | 0.046 | 1.8 | 5.4% | 6303 |
| metrics insert | 16.7 | 0.017 | 0.070 | 1.2 | 3.4% | 20211 |
| metrics prune | 16.7 | 0.017 | 0.017 | 0.3 | 0.8% | 14239 |
| lockAgentCredential | 16.7 | 0.017 | 0.017 | 0.3 | 0.8% | 1512 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.044 | 0.1 | 0.2% | 279 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.002 | 0.023 | 0.0 | 0.1% | 0 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.168 | 0.0 | 0.1% | 263 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.069 | 0.0 | 0.1% | 99 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.109 | 0.0 | 0.1% | 293 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 1.269 | 0.0 | 0.1% | 870 |

WAL fsync: 110/s, mean 0.00 ms. Database: 642102975 → 645174975 bytes (6398 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.028 cores. Load 3.87 5.17 5.47 → 3.68 4.05 4.74.

Largest table growth: server_metrics +2908160 (now 628695040); river_job +81920 (now 319488); workspaces +8192 (now 98304); servers +0 (now 3293184); audit_events +0 (now 385024); auth_attempts +0 (now 262144); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 155.
Oldest kept error: 22:53:04 metrics: Post "http://fleet-app-f7fbde57:8080/agent/v1/metrics": context deadline exceeded

### tmpfs-idle-1100-rtt3-pool12

Window 480s; revision d1a9b46; ping round-trip min/avg/max = 2.899/3.542/3.994 ms; options map[agents:1100 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:2800us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 poolSize:12 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 1224.2 | 1.113 | 0.011 | 14.0 | 40.4% | 3595 |
| ListTroubleshootingOffers | 1089.1 | 0.990 | 0.006 | 6.5 | 18.8% | 0 |
| poll command | 1089.1 | 0.990 | 0.004 | 4.4 | 12.6% | 0 |
| RecordAgentHeartbeat | 73.3 | 0.067 | 0.048 | 3.5 | 10.1% | 11921 |
| poll lease take | 43.5 | 0.040 | 0.046 | 2.0 | 5.8% | 6818 |
| poll lease release | 43.5 | 0.040 | 0.046 | 2.0 | 5.7% | 6159 |
| metrics insert | 18.3 | 0.017 | 0.072 | 1.3 | 3.8% | 22939 |
| metrics prune | 18.3 | 0.017 | 0.021 | 0.4 | 1.1% | 21519 |
| lockAgentCredential | 18.3 | 0.017 | 0.017 | 0.3 | 0.9% | 2447 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.002 | 0.032 | 0.1 | 0.2% | 0 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.039 | 0.0 | 0.1% | 294 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 1.531 | 0.0 | 0.1% | 1033 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.125 | 0.0 | 0.1% | 253 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.069 | 0.0 | 0.1% | 101 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.096 | 0.0 | 0.1% | 312 |

WAL fsync: 139/s, mean 0.00 ms. Database: 704280255 → 707614399 bytes (6944 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.028 cores. Load 9.23 5.60 4.51 → 6.49 5.19 4.67.

Largest table growth: server_metrics +3203072 (now 690216960); river_job +57344 (now 294912); workspaces +8192 (now 139264); servers +0 (now 3981312); audit_events +0 (now 434176); auth_attempts +0 (now 303104); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 197.
Oldest kept error: 11:25:07 metrics: Post "http://fleet-app-00084ffc:8080/agent/v1/metrics": context deadline exceeded

Pool log in the window:

```
2026/11/06 11:27:27 INFO database pool size=12 open=12 inUse=11 acquires=215494 waited=169531 waitSeconds=2084.171069826 canceled=0
2026/11/06 11:28:27 INFO database pool size=12 open=12 inUse=11 acquires=214583 waited=157614 waitSeconds=1682.239380977 canceled=0
2026/11/06 11:29:27 INFO database pool size=12 open=12 inUse=11 acquires=214741 waited=151594 waitSeconds=804.358229991 canceled=0
2026/11/06 11:30:27 INFO database pool size=12 open=12 inUse=12 acquires=215135 waited=163690 waitSeconds=2215.303906134 canceled=0
2026/11/06 11:31:27 INFO database pool size=12 open=12 inUse=12 acquires=214223 waited=161364 waitSeconds=2520.451587169 canceled=0
2026/11/06 11:32:27 INFO database pool size=12 open=12 inUse=12 acquires=215222 waited=157432 waitSeconds=559.197763661 canceled=0
2026/11/06 11:33:27 INFO database pool size=12 open=12 inUse=12 acquires=215114 waited=154933 waitSeconds=1213.384072298 canceled=0
2026/11/06 11:34:27 INFO database pool size=12 open=12 inUse=9 acquires=214380 waited=147394 waitSeconds=440.647252019 canceled=0
```

Application warnings:

```
1 2026/11/06 11:25:16 ERROR request failed request_id=bbd0a7d8-c719-43bf-bb1b-149798edf9f0 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:16 ERROR request failed request_id=96d8f356-fc3c-4e45-ae3e-42caf6ca5ffc route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:15 ERROR request failed request_id=df4298ca-b19a-4dfc-a6cc-00beab523599 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:15 ERROR request failed request_id=d467ff4b-05fc-406a-a8f9-4d13a4370b13 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:15 ERROR request failed request_id=5391d93b-3fbc-489e-a3f5-a98c4b678b53 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:14 ERROR request failed request_id=b2541cea-bbdb-4818-bbd5-d9517c59f8d0 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:14 ERROR request failed request_id=a8e7619e-3b44-40e1-a3de-9099813fdc9f route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:14 ERROR request failed request_id=9717b9dc-2050-422d-a8cd-174beb50d64e route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:14 ERROR request failed request_id=544dcc8b-d992-49f6-b6ad-fe32f2ec9555 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:14 ERROR request failed request_id=487c6e40-60f7-4b83-9a6e-ea467f8488fa route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:14 ERROR request failed request_id=476ed550-a6fd-4d11-b5d7-dbfc92257eb8 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:14 ERROR request failed request_id=3bb2f06c-085c-4540-898c-3abad4565fa7 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:14 ERROR request failed request_id=36367908-b578-4024-9948-2c5010dd70d1 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:13 ERROR request failed request_id=eb46057c-212c-45ed-93fc-1a8d5d08c89c route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:13 ERROR request failed request_id=c5e18b89-a44f-4a88-855c-c08bc9190398 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:13 ERROR request failed request_id=af928598-826d-4080-8258-e8951278fe8c route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:13 ERROR request failed request_id=618a0e84-e114-440b-bf61-78edafda64db route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:13 ERROR request failed request_id=5e2fdb25-ca61-48d1-bbb7-dfb95ea49390 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:13 ERROR request failed request_id=3f74ea0f-f9e4-48a4-93b7-5aed50283872 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:25:13 ERROR request failed request_id=2c100c3f-4799-4ee1-a28f-6968b2becd95 route=/agent/v1/poll error.category=context_canceled
```

### tmpfs-idle-1500-repeat

Window 480s; revision d1a9b46; ping round-trip min/avg/max = 0.372/0.489/4.385 ms; options map[agents:1500 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 poolSize:4 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 1674.5 | 1.116 | 0.010 | 16.6 | 38.7% | 5767 |
| ListTroubleshootingOffers | 1489.9 | 0.993 | 0.005 | 7.5 | 17.4% | 0 |
| metrics prune | 25.2 | 0.017 | 0.178 | 4.5 | 10.5% | 8643 |
| poll command | 1489.9 | 0.993 | 0.003 | 4.5 | 10.4% | 0 |
| RecordAgentHeartbeat | 100.0 | 0.067 | 0.038 | 3.8 | 8.8% | 16406 |
| poll lease release | 59.4 | 0.040 | 0.036 | 2.1 | 4.9% | 9676 |
| poll lease take | 59.4 | 0.040 | 0.034 | 2.0 | 4.7% | 9511 |
| metrics insert | 25.2 | 0.017 | 0.055 | 1.4 | 3.2% | 32887 |
| lockAgentCredential | 25.2 | 0.017 | 0.013 | 0.3 | 0.8% | 3592 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.034 | 0.0 | 0.1% | 269 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.021 | 0.0 | 0.1% | 0 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.136 | 0.0 | 0.1% | 252 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.075 | 0.0 | 0.1% | 140 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.088 | 0.0 | 0.0% | 323 |
| BEGIN | 26.0 | 0.017 | 0.001 | 0.0 | 0.0% | 0 |

WAL fsync: 152/s, mean 0.00 ms. Database: 965670591 → 970331839 bytes (9709 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.032 cores. Load 5.24 6.18 6.57 → 3.89 5.13 5.92.

Largest table growth: server_metrics +4521984 (now 949411840); river_job +57344 (now 294912); workspaces +8192 (now 114688); servers +0 (now 7405568); audit_events +0 (now 532480); auth_attempts +0 (now 360448); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: 12:06:46 metrics: Post "http://fleet-app-a27c3dea:8080/agent/v1/metrics": context deadline exceeded

Pool log in the window:

```
2026/11/06 12:08:52 INFO database pool size=4 open=4 inUse=4 acquires=292586 waited=148277 waitSeconds=2493.879861211 canceled=0
2026/11/06 12:09:52 INFO database pool size=4 open=4 inUse=1 acquires=295460 waited=127744 waitSeconds=85.399345803 canceled=0
2026/11/06 12:10:52 INFO database pool size=4 open=4 inUse=3 acquires=292667 waited=136437 waitSeconds=435.123347236 canceled=0
2026/11/06 12:11:52 INFO database pool size=4 open=4 inUse=3 acquires=294479 waited=168527 waitSeconds=836.635373659 canceled=0
2026/11/06 12:12:52 INFO database pool size=4 open=4 inUse=4 acquires=293548 waited=161187 waitSeconds=344.821175284 canceled=0
2026/11/06 12:13:52 INFO database pool size=4 open=4 inUse=2 acquires=292653 waited=149415 waitSeconds=307.574838124 canceled=0
2026/11/06 12:14:52 INFO database pool size=4 open=4 inUse=1 acquires=295321 waited=153662 waitSeconds=487.382717388 canceled=0
2026/11/06 12:15:52 INFO database pool size=4 open=4 inUse=4 acquires=293334 waited=132208 waitSeconds=76.026667747 canceled=0
```

Application warnings:

```
1 2026/11/06 12:07:08 ERROR request failed request_id=ffd25040-70c7-489d-bcec-4638d43c794c route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=f8c598cf-401f-4992-b78c-1d45e90f1087 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=f12fdcfb-dc50-4b95-9b35-b4c92e0282f0 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=eb5bad6b-4d0a-4efe-8c5d-a847a0367f8a route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=e948c3a8-5156-466d-b6e4-35d01b9f288c route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=e67f6430-a474-423d-bb7c-cd1ae48d1452 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=e14ed345-94d7-4fc1-aee5-1943a83f8e8b route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=d6a1798d-7ad0-4aa2-b21a-2d1170411e6d route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=ceb9c0d1-e0db-4539-962a-84c52d973b8d route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=ccfc6843-3227-47a9-af05-39df9bfcf46b route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=cc5022cc-a36f-4799-9961-9fa2ed3518bf route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=c9f35943-9ea5-481e-a687-37b7b06870e0 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=c8c61b17-e9a6-4086-8069-50dbe0d0d9bf route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=c6033971-5f76-45fe-b82a-444b317adb67 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=bbf68838-4e9a-40cb-8e93-3fe5633f6ec4 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=bbc0f366-5d7a-44b8-933b-b9bdfab836e5 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=b3737a5f-5e08-4af2-8340-3a9aa7817a42 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=b2d401f0-2102-4e7a-90ce-d939dfd015b2 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=97c1f771-651b-44c5-a125-92c48933ef25 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:07:08 ERROR request failed request_id=96dc0ac4-1f67-4fbb-96b2-ea177d623b8e route=/agent/v1/poll error.category=context_canceled
```

### tmpfs-idle-1500-rtt3-pool12

Window 480s; revision d1a9b46; ping round-trip min/avg/max = 2.877/3.483/3.705 ms; options map[agents:1500 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:2800us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 poolSize:12 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 1295.9 | 0.864 | 0.015 | 19.4 | 37.6% | 3861 |
| ListTroubleshootingOffers | 1121.6 | 0.748 | 0.008 | 9.4 | 18.2% | 0 |
| poll command | 1120.2 | 0.747 | 0.005 | 5.9 | 11.4% | 0 |
| RecordAgentHeartbeat | 94.8 | 0.063 | 0.059 | 5.6 | 10.9% | 17297 |
| poll lease take | 53.1 | 0.035 | 0.059 | 3.1 | 6.0% | 9238 |
| poll lease release | 53.0 | 0.035 | 0.057 | 3.0 | 5.9% | 8827 |
| metrics prune | 24.7 | 0.016 | 0.089 | 2.2 | 4.3% | 20901 |
| metrics insert | 24.7 | 0.016 | 0.082 | 2.0 | 3.9% | 31473 |
| lockAgentCredential | 24.7 | 0.016 | 0.023 | 0.6 | 1.1% | 2669 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.039 | 0.1 | 0.1% | 0 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.054 | 0.1 | 0.1% | 287 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.103 | 0.0 | 0.1% | 99 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.122 | 0.0 | 0.0% | 264 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.002 | 0.007 | 0.0 | 0.0% | 0 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.114 | 0.0 | 0.0% | 290 |

WAL fsync: 175/s, mean 0.00 ms. Database: 966121151 → 970749631 bytes (9640 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.050 cores. Load 14.50 7.81 5.84 → 6.56 7.61 6.62.

Largest table growth: server_metrics +4497408 (now 949592064); river_job +57344 (now 294912); workspaces +8192 (now 155648); servers +0 (now 7536640); audit_events +0 (now 557056); auth_attempts +0 (now 393216); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: 11:48:27 poll: Get "http://fleet-app-8119bbcc:8080/agent/v1/poll": net/http: timeout awaiting response headers

Pool log in the window:

```
2026/11/06 11:40:59 INFO database pool size=12 open=12 inUse=11 acquires=207218 waited=207218 waitSeconds=94856.904942005 canceled=391
2026/11/06 11:41:59 INFO database pool size=12 open=12 inUse=12 acquires=222090 waited=222090 waitSeconds=94582.434454839 canceled=383
2026/11/06 11:42:59 INFO database pool size=12 open=12 inUse=11 acquires=225535 waited=225535 waitSeconds=94597.678682108 canceled=252
2026/11/06 11:43:59 INFO database pool size=12 open=12 inUse=11 acquires=228460 waited=228460 waitSeconds=94063.224096214 canceled=279
2026/11/06 11:44:59 INFO database pool size=12 open=12 inUse=11 acquires=223828 waited=223828 waitSeconds=94341.246271785 canceled=259
2026/11/06 11:45:59 INFO database pool size=12 open=12 inUse=12 acquires=227510 waited=227510 waitSeconds=94298.469239988 canceled=299
2026/11/06 11:46:59 INFO database pool size=12 open=12 inUse=12 acquires=226464 waited=226464 waitSeconds=94349.192989527 canceled=323
2026/11/06 11:47:59 INFO database pool size=12 open=12 inUse=12 acquires=227177 waited=227177 waitSeconds=94156.676761208 canceled=261
```

Application warnings:

```
1 2026/11/06 11:48:59 ERROR request failed request_id=c5d73579-4678-4216-8328-51fc59cca30b route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:58 ERROR request failed request_id=dc1835ee-6287-4b17-bfe4-8ad24b26abea route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:58 ERROR request failed request_id=c4f766af-d2b7-4b59-a950-f06f27a71e29 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:58 ERROR request failed request_id=6aaf8e40-7a45-46de-a1b1-2eb8dc82e387 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:57 ERROR request failed request_id=f99c0d8e-89bf-443b-a83c-2ccb93913ecb route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:57 ERROR request failed request_id=a0b36e71-7136-4fd1-aeda-5f4b9341271d route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:57 ERROR request failed request_id=93642971-34bb-45f3-9e2b-22e4f0db225c route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:57 ERROR request failed request_id=6d9c5db2-281e-440e-bace-fdec8ec602da route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:57 ERROR request failed request_id=69408aed-6bf8-4f02-8ed3-719ba5afc4c2 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:57 ERROR request failed request_id=4fe622f1-a71a-49f7-94d3-0463626b8573 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:57 ERROR request failed request_id=33677df5-4aa2-4f91-95a4-a3d6e376a3f1 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:56 ERROR request failed request_id=da95a239-43d1-43b1-a5c0-b58cf339a8fb route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:56 ERROR request failed request_id=d1de3159-6a82-407b-befa-057901bfb914 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:56 ERROR request failed request_id=75810e4b-ebba-4b35-8b2b-084b4794fae9 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:56 ERROR request failed request_id=5a581ffc-06d2-427e-b9ad-54e8be57bd35 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:56 ERROR request failed request_id=182016c3-eb1b-454b-802e-dff06c1bc424 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:56 ERROR request failed request_id=11403e03-9dd0-41c2-b831-3a5122c77a82 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:55 ERROR request failed request_id=db440eaa-1e7d-40b3-a0a6-41a32fc8795c route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:55 ERROR request failed request_id=982d804e-c0e7-4356-b4dc-4e2a131e4299 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:48:55 ERROR request failed request_id=8b8a2f42-6e86-4a69-b2a9-768310e205d0 route=/agent/v1/poll error.category=context_canceled
```

### tmpfs-idle-1500-rtt3-pool24

Window 480s; revision d1a9b46; ping round-trip min/avg/max = 2.856/3.477/3.696 ms; options map[agents:1500 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:2800us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 poolSize:24 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 1672.5 | 1.115 | 0.017 | 27.8 | 42.1% | 4374 |
| ListTroubleshootingOffers | 1488.0 | 0.992 | 0.009 | 13.3 | 20.1% | 0 |
| poll command | 1488.1 | 0.992 | 0.006 | 8.6 | 13.0% | 0 |
| RecordAgentHeartbeat | 100.1 | 0.067 | 0.058 | 5.9 | 8.9% | 19705 |
| poll lease release | 59.4 | 0.040 | 0.059 | 3.5 | 5.3% | 9590 |
| poll lease take | 59.4 | 0.040 | 0.056 | 3.4 | 5.1% | 9445 |
| metrics insert | 25.0 | 0.017 | 0.083 | 2.1 | 3.2% | 33318 |
| metrics prune | 25.0 | 0.017 | 0.029 | 0.7 | 1.1% | 15737 |
| lockAgentCredential | 25.0 | 0.017 | 0.023 | 0.6 | 0.9% | 3557 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.034 | 0.1 | 0.1% | 0 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.046 | 0.1 | 0.1% | 266 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.098 | 0.0 | 0.0% | 105 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.129 | 0.0 | 0.0% | 254 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.002 | 0.008 | 0.0 | 0.0% | 0 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 1.381 | 0.0 | 0.0% | 694 |

WAL fsync: 144/s, mean 0.00 ms. Database: 970979007 → 975607487 bytes (9640 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.044 cores. Load 9.85 8.77 7.42 → 5.95 6.50 6.82.

Largest table growth: server_metrics +4448256 (now 949501952); river_job +90112 (now 327680); workspaces +8192 (now 155648); servers +0 (now 12386304); audit_events +0 (now 557056); auth_attempts +0 (now 376832); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: 11:53:04 metrics: Post "http://fleet-app-934d0e9a:8080/agent/v1/metrics": context deadline exceeded

Pool log in the window:

```
2026/11/06 11:55:45 INFO database pool size=24 open=24 inUse=19 acquires=293878 waited=59711 waitSeconds=255.63157197499999 canceled=0
2026/11/06 11:56:45 INFO database pool size=24 open=24 inUse=23 acquires=293370 waited=40854 waitSeconds=40.232440145 canceled=0
2026/11/06 11:57:45 INFO database pool size=24 open=24 inUse=15 acquires=293686 waited=33366 waitSeconds=21.739466395 canceled=0
2026/11/06 11:58:45 INFO database pool size=24 open=24 inUse=8 acquires=293274 waited=42757 waitSeconds=178.276943191 canceled=0
2026/11/06 11:59:45 INFO database pool size=24 open=24 inUse=17 acquires=293342 waited=29530 waitSeconds=17.759618496 canceled=0
2026/11/06 12:00:45 INFO database pool size=24 open=24 inUse=10 acquires=293963 waited=33807 waitSeconds=30.650689543 canceled=0
2026/11/06 12:01:45 INFO database pool size=24 open=24 inUse=18 acquires=293376 waited=31515 waitSeconds=51.360875657 canceled=0
2026/11/06 12:02:45 INFO database pool size=24 open=24 inUse=8 acquires=292940 waited=34379 waitSeconds=119.331293085 canceled=0
```

Application warnings:

```
1 2026/11/06 11:53:09 ERROR request failed request_id=adf47029-aa84-407f-bfee-04aa8b46837d route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:08 ERROR request failed request_id=d4daea42-e9f9-482a-a688-b2088f685ca2 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:08 ERROR request failed request_id=59332943-021a-45fe-9c7d-e592ec34b68c route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=f955b88e-e9d8-4826-b4e0-1b594c0ffb20 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=ed522f34-f523-441e-92ee-49b6427f4701 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=e74bedd2-9e39-407e-b565-ad2763025c93 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=e0de9a64-3025-4f6b-847a-56ff74f5e4db route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=db7e33a9-2bff-404b-99de-0bdf2c9f3685 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=d90af0f9-2b1a-47b9-9462-171534240b9c route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=d5beb8bc-da40-41fc-a990-48f3eb3da2ba route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=c2f04a47-5612-4863-9e6c-7699379b58ad route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=b0318551-7e4c-436f-a200-7a80216ac0fb route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=aa5875c1-0a26-4cf4-a72c-6d6699e60e18 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=a5b6f41e-c9f2-4121-96d0-f21ffb2d73a4 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=977cd027-26c7-42aa-aa01-2f04932f92ab route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=7a6ca363-a7ca-4681-97da-b7b80a59edd4 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=76534d5c-8729-4d57-8e03-bb7368eecf14 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=72ec0be8-1260-4a68-a21f-5aa73778a983 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=6a8d39c2-f375-4a14-9def-c43e98f54f78 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 11:53:07 ERROR request failed request_id=61a9ca3e-46b4-4ba4-8815-bace09120100 route=/agent/v1/poll error.category=context_canceled
```

### tmpfs-idle-1500-slow-db

Window 480s; revision f06a5b8; ping round-trip min/avg/max = 0.581/0.748/1.215 ms; options map[agents:1500 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:500us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 1666.6 | 1.111 | 0.012 | 20.8 | 38.1% | 5639 |
| ListTroubleshootingOffers | 1482.3 | 0.988 | 0.007 | 9.9 | 18.1% | 0 |
| poll command | 1482.2 | 0.988 | 0.004 | 5.9 | 10.9% | 0 |
| RecordAgentHeartbeat | 99.9 | 0.067 | 0.052 | 5.2 | 9.5% | 16724 |
| metrics prune | 25.0 | 0.017 | 0.191 | 4.8 | 8.8% | 8282 |
| poll lease release | 59.4 | 0.040 | 0.047 | 2.8 | 5.1% | 8921 |
| poll lease take | 59.4 | 0.040 | 0.046 | 2.8 | 5.1% | 9436 |
| metrics insert | 25.0 | 0.017 | 0.068 | 1.7 | 3.1% | 32487 |
| lockAgentCredential | 25.0 | 0.017 | 0.017 | 0.4 | 0.8% | 3244 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.043 | 0.1 | 0.1% | 263 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.024 | 0.0 | 0.1% | 0 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.158 | 0.0 | 0.1% | 252 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.082 | 0.0 | 0.0% | 123 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.097 | 0.0 | 0.0% | 299 |
| BEGIN | 25.8 | 0.017 | 0.001 | 0.0 | 0.0% | 0 |

WAL fsync: 190/s, mean 0.00 ms. Database: 965613247 → 970307263 bytes (9777 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.046 cores. Load 6.17 6.15 5.73 → 9.20 6.44 5.82.

Largest table growth: server_metrics +4546560 (now 949370880); river_job +65536 (now 303104); workspaces +8192 (now 114688); servers +0 (now 7421952); audit_events +0 (now 532480); auth_attempts +0 (now 385024); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: 22:39:42 poll: Get "http://fleet-app-414bc8da:8080/agent/v1/poll": net/http: timeout awaiting response headers

### tmpfs-idle-1500

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.396/0.556/1.861 ms; options map[agents:1500 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 1711.8 | 1.141 | 0.010 | 17.3 | 43.5% | 4973 |
| ListTroubleshootingOffers | 1524.4 | 1.016 | 0.005 | 7.6 | 19.3% | 0 |
| poll command | 1524.4 | 1.016 | 0.003 | 4.6 | 11.5% | 0 |
| RecordAgentHeartbeat | 100.0 | 0.067 | 0.034 | 3.4 | 8.5% | 15242 |
| poll lease release | 61.8 | 0.041 | 0.037 | 2.3 | 5.8% | 10640 |
| poll lease take | 61.7 | 0.041 | 0.035 | 2.2 | 5.5% | 9934 |
| metrics insert | 25.6 | 0.017 | 0.054 | 1.4 | 3.5% | 32080 |
| metrics prune | 25.6 | 0.017 | 0.014 | 0.4 | 0.9% | 12001 |
| lockAgentCredential | 25.6 | 0.017 | 0.013 | 0.3 | 0.9% | 2900 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.041 | 0.0 | 0.1% | 284 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 2.225 | 0.0 | 0.1% | 1811 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.020 | 0.0 | 0.1% | 0 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.142 | 0.0 | 0.1% | 295 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.068 | 0.0 | 0.1% | 125 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.090 | 0.0 | 0.0% | 313 |

WAL fsync: 150/s, mean 0.00 ms. Database: 964523711 → 969094847 bytes (9521 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.030 cores. Load 3.98 5.98 6.32 → 4.44 4.82 5.58.

Largest table growth: server_metrics +4423680 (now 949796864); river_job +73728 (now 311296); workspaces +8192 (now 114688); servers +0 (now 5816320); audit_events +0 (now 524288); auth_attempts +0 (now 368640); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: poll: Get "http://fleet-app-222c7e10:8080/agent/v1/poll": net/http: timeout awaiting response headers

Application warnings:

```
1 time=2026-11-05T18:34:23.276Z level=ERROR msg="maintenance.PeriodicJobEnqueuer: Error starting transaction" error="context canceled"
      1 time=2026-11-05T18:34:21.214Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T18:34:20.996Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=1 client_id=de5f65f8fee5_2026_11_05T18_31_29_854278 err="leadership.Elector.attemptResign timed out after 1s: context deadline exceeded" 
      1 time=2026-11-05T18:34:19.996Z level=WARN msg="leadership.Elector: Current leader stepping down because the reelection deadline elapsed after an error" client_id=de5f65f8fee5_2026_11_05T18_31_29_854278
      1 time=2026-11-05T18:34:15.998Z level=ERROR msg="leadership.Elector: Error attempting reelection" attempt=1 client_id=de5f65f8fee5_2026_11_05T18_31_29_854278 err="leadership.attemptReelect timed out after 5s: context deadline exceeded" sleep_
```

### tmpfs-idle-2000-contended

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.385/0.534/1.912 ms; options map[agents:2000 appCPUs: appCpuset:0-3 appMemory: dbCPUs: dbCpuset:4-7 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 2062.9 | 1.031 | 0.018 | 37.4 | 41.1% | 6800 |
| ListTroubleshootingOffers | 1817.2 | 0.909 | 0.010 | 18.4 | 20.2% | 0 |
| poll command | 1816.1 | 0.908 | 0.006 | 10.5 | 11.5% | 0 |
| RecordAgentHeartbeat | 132.0 | 0.066 | 0.070 | 9.3 | 10.2% | 21016 |
| poll lease take | 78.1 | 0.039 | 0.068 | 5.3 | 5.9% | 13421 |
| poll lease release | 72.6 | 0.036 | 0.066 | 4.8 | 5.3% | 10393 |
| metrics insert | 33.5 | 0.017 | 0.091 | 3.0 | 3.3% | 41592 |
| lockAgentCredential | 33.5 | 0.017 | 0.028 | 0.9 | 1.0% | 7321 |
| metrics prune | 33.6 | 0.017 | 0.027 | 0.9 | 1.0% | 26740 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.034 | 0.1 | 0.1% | 0 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.047 | 0.1 | 0.1% | 340 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 3.081 | 0.1 | 0.1% | 3410 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.125 | 0.0 | 0.0% | 126 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.195 | 0.0 | 0.0% | 291 |
| UPDATE operational_alerts SET state='resolved',reason=$4,res… | 3.3 | 0.002 | 0.008 | 0.0 | 0.0% | 0 |

WAL fsync: 218/s, mean 0.00 ms. Database: 1277793983 → 1283798719 bytes (12500 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.081 cores. Load 18.32 20.11 18.42 → 32.88 18.27 16.89.

Largest table growth: server_metrics +5832704 (now 1257472000); river_job +57344 (now 294912); river_leader +32768 (now 81920); servers +0 (now 12574720); audit_events +0 (now 671744); auth_attempts +0 (now 450560); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: poll: Get "http://fleet-app-3c86ee6f:8080/agent/v1/poll": net/http: timeout awaiting response headers

Application warnings:

```
1 time=2026-11-05T16:16:38.140Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T16:16:34.623Z level=ERROR msg="maintenance.PeriodicJobEnqueuer: Error starting transaction" error="context canceled"
      1 time=2026-11-05T16:16:30.422Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=3 client_id=be4a3c1b7a03_2026_11_05T16_13_16_037751 err="leadership.Elector.attemptResign timed out after 3s: context deadline exceeded" 
      1 time=2026-11-05T16:16:28.312Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T16:16:25.527Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=2 client_id=be4a3c1b7a03_2026_11_05T16_13_16_037751 err="leadership.Elector.attemptResign timed out after 2s: context deadline exceeded" 
      1 time=2026-11-05T16:16:22.507Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=1 client_id=be4a3c1b7a03_2026_11_05T16_13_16_037751 err="leadership.Elector.attemptResign timed out after 1s: context deadline exceeded" 
      1 time=2026-11-05T16:16:21.506Z level=WARN msg="leadership.Elector: Current leader stepping down because the reelection deadline elapsed after an error" client_id=be4a3c1b7a03_2026_11_05T16_13_16_037751
      1 time=2026-11-05T16:16:18.484Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T16:16:17.784Z level=ERROR msg="leadership.Elector: Error attempting reelection" attempt=1 client_id=be4a3c1b7a03_2026_11_05T16_13_16_037751 err="leadership.attemptReelect timed out after 5s: context deadline exceeded" sleep_
```

### tmpfs-idle-2000-no-tick

Window 480s; revision f06a5b8; ping round-trip min/avg/max = 0.400/0.525/0.643 ms; options map[agents:2000 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 serverPatched:true services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| RecordAgentHeartbeat | 133.3 | 0.067 | 0.068 | 9.1 | 25.4% | 22648 |
| AuthenticateAgent | 325.2 | 0.163 | 0.025 | 8.2 | 22.8% | 8255 |
| poll lease release | 79.2 | 0.040 | 0.074 | 5.8 | 16.3% | 13717 |
| poll lease take | 79.3 | 0.040 | 0.063 | 5.0 | 14.0% | 12941 |
| metrics insert | 33.3 | 0.017 | 0.106 | 3.5 | 9.9% | 42757 |
| metrics prune | 33.3 | 0.017 | 0.032 | 1.1 | 3.0% | 34627 |
| ListTroubleshootingOffers | 79.3 | 0.040 | 0.012 | 0.9 | 2.6% | 0 |
| lockAgentCredential | 33.3 | 0.017 | 0.027 | 0.9 | 2.5% | 4494 |
| poll command | 79.3 | 0.040 | 0.009 | 0.7 | 2.1% | 0 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.094 | 0.1 | 0.3% | 359 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.326 | 0.1 | 0.2% | 270 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.037 | 0.1 | 0.2% | 0 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.172 | 0.1 | 0.2% | 140 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 3.303 | 0.1 | 0.2% | 708 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.204 | 0.0 | 0.1% | 258 |

WAL fsync: 155/s, mean 0.00 ms. Database: 1277392575 → 1283389119 bytes (12490 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.066 cores. Load 2.79 3.29 4.12 → 2.91 2.71 3.39.

Largest table growth: server_metrics +5840896 (now 1257578496); river_job +81920 (now 319488); servers +0 (now 12034048); audit_events +0 (now 655360); auth_attempts +0 (now 450560); sessions +0 (now 237568); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: 23:06:53 metrics: Post "http://fleet-app-6c10b4ec:8080/agent/v1/metrics": context deadline exceeded

### tmpfs-idle-2000-pool12

Window 480s; revision 35aa720; ping round-trip min/avg/max = 0.340/0.495/2.955 ms; options map[agents:2000 appCPUs: appCpuset:0-11 appMemory: dbCPUs: dbCpuset:0-11 dbMemory: dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 2275.7 | 1.138 | 0.016 | 36.3 | 44.0% | 6961 |
| ListTroubleshootingOffers | 2027.6 | 1.014 | 0.008 | 17.1 | 20.7% | 0 |
| poll command | 2027.6 | 1.014 | 0.005 | 10.1 | 12.3% | 0 |
| RecordAgentHeartbeat | 133.3 | 0.067 | 0.047 | 6.3 | 7.6% | 21333 |
| poll lease release | 80.9 | 0.040 | 0.054 | 4.4 | 5.3% | 11942 |
| poll lease take | 80.9 | 0.040 | 0.052 | 4.2 | 5.1% | 12819 |
| metrics insert | 33.9 | 0.017 | 0.071 | 2.4 | 2.9% | 41250 |
| metrics prune | 33.9 | 0.017 | 0.023 | 0.8 | 1.0% | 27173 |
| lockAgentCredential | 33.9 | 0.017 | 0.019 | 0.7 | 0.8% | 4745 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.053 | 0.1 | 0.1% | 281 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.026 | 0.0 | 0.1% | 0 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.108 | 0.0 | 0.0% | 113 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.157 | 0.0 | 0.0% | 262 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.107 | 0.0 | 0.0% | 386 |
| UPDATE operational_alerts SET state=$6,reason=$4,resolved_at… | 3.3 | 0.002 | 0.006 | 0.0 | 0.0% | 0 |

WAL fsync: 132/s, mean 0.00 ms. Database: 1281193663 → 1287321279 bytes (12763 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.051 cores. Load 10.49 14.74 15.70 → 10.57 10.83 13.21.

Largest table growth: server_metrics +5971968 (now 1258250240); river_job +65536 (now 311296); workspaces +8192 (now 163840); servers +0 (now 15122432); audit_events +0 (now 704512); auth_attempts +0 (now 483328); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: poll: Get "http://fleet-app-aefa0190:8080/agent/v1/poll": net/http: timeout awaiting response headers

Application warnings:

```
1 time=2026-11-05T16:31:23.467Z level=ERROR msg="maintenance.PeriodicJobEnqueuer: Error starting transaction" error="context canceled"
      1 time=2026-11-05T16:31:20.060Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T16:31:19.607Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=3 client_id=8c2a119ee522_2026_11_05T16_27_59_348958 err="leadership.Elector.attemptResign timed out after 3s: context deadline exceeded" 
      1 time=2026-11-05T16:31:14.587Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=2 client_id=8c2a119ee522_2026_11_05T16_27_59_348958 err="leadership.Elector.attemptResign timed out after 2s: context deadline exceeded" 
      1 time=2026-11-05T16:31:11.684Z level=ERROR msg="leadership.Elector: Error attempting to resign" attempt=1 client_id=8c2a119ee522_2026_11_05T16_27_59_348958 err="leadership.Elector.attemptResign timed out after 1s: context deadline exceeded" 
      1 time=2026-11-05T16:31:10.862Z level=WARN msg="leadership.Elector: Current leader stepping down because the reelection deadline elapsed after an error" client_id=8c2a119ee522_2026_11_05T16_27_59_348958
      1 time=2026-11-05T16:31:10.237Z level=ERROR msg="producer: Error fetching jobs" err="StandardPilot.JobGetAvailable timed out after 10s: context deadline exceeded" queue=default
      1 time=2026-11-05T16:31:06.863Z level=ERROR msg="leadership.Elector: Error attempting reelection" attempt=1 client_id=8c2a119ee522_2026_11_05T16_27_59_348958 err="leadership.attemptReelect timed out after 5s: context deadline exceeded" sleep_
```

### tmpfs-idle-2000-pool48

Window 480s; revision d1a9b46; ping round-trip min/avg/max = 0.331/0.419/1.088 ms; options map[agents:2000 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 poolSize:48 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 2236.5 | 1.118 | 0.021 | 46.4 | 43.5% | 6449 |
| ListTroubleshootingOffers | 1990.5 | 0.995 | 0.010 | 20.7 | 19.4% | 0 |
| poll command | 1990.5 | 0.995 | 0.007 | 13.7 | 12.9% | 0 |
| RecordAgentHeartbeat | 133.4 | 0.067 | 0.070 | 9.4 | 8.8% | 23140 |
| poll lease release | 79.2 | 0.040 | 0.070 | 5.6 | 5.2% | 13574 |
| poll lease take | 79.2 | 0.040 | 0.064 | 5.1 | 4.8% | 12887 |
| metrics insert | 33.4 | 0.017 | 0.103 | 3.4 | 3.2% | 44044 |
| metrics prune | 33.4 | 0.017 | 0.029 | 1.0 | 0.9% | 24193 |
| lockAgentCredential | 33.4 | 0.017 | 0.028 | 0.9 | 0.9% | 5600 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 4.086 | 0.1 | 0.1% | 1800 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.035 | 0.1 | 0.1% | 0 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.047 | 0.1 | 0.1% | 272 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.139 | 0.0 | 0.0% | 148 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.149 | 0.0 | 0.0% | 264 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.142 | 0.0 | 0.0% | 390 |

WAL fsync: 131/s, mean 0.00 ms. Database: 1280726719 → 1286706879 bytes (12455 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.060 cores. Load 5.04 5.33 5.76 → 15.09 13.05 9.33.

Largest table growth: server_metrics +5824512 (now 1258078208); river_job +73728 (now 311296); workspaces +8192 (now 155648); servers +0 (now 14688256); audit_events +0 (now 679936); auth_attempts +0 (now 458752); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: 12:20:33 poll: Get "http://fleet-app-cd7a8dbf:8080/agent/v1/poll": net/http: timeout awaiting response headers

Pool log in the window:

```
2026/11/06 12:23:33 INFO database pool size=48 open=48 inUse=7 acquires=394911 waited=11639 waitSeconds=4.763391483 canceled=0
2026/11/06 12:24:33 INFO database pool size=48 open=48 inUse=27 acquires=390919 waited=9124 waitSeconds=2.158502952 canceled=0
2026/11/06 12:25:33 INFO database pool size=48 open=48 inUse=7 acquires=392495 waited=13060 waitSeconds=10.584733931 canceled=0
2026/11/06 12:26:33 INFO database pool size=48 open=48 inUse=1 acquires=394083 waited=16308 waitSeconds=18.606539906 canceled=0
2026/11/06 12:27:33 INFO database pool size=48 open=48 inUse=1 acquires=390743 waited=10263 waitSeconds=11.323950671 canceled=0
2026/11/06 12:28:33 INFO database pool size=48 open=48 inUse=0 acquires=392741 waited=13067 waitSeconds=13.526542671 canceled=0
2026/11/06 12:29:33 INFO database pool size=48 open=48 inUse=11 acquires=392361 waited=35731 waitSeconds=137.686870156 canceled=0
2026/11/06 12:30:33 INFO database pool size=48 open=48 inUse=1 acquires=391360 waited=4243 waitSeconds=7.197130526 canceled=0
```

Application warnings:

```
1 2026/11/06 12:20:35 ERROR request failed request_id=f5895fdd-735b-445b-963e-73f9c025536f route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=e88de58f-778e-4394-b5ca-bf58f045dbf4 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=e85d132d-a8e2-4c9c-a012-855b9db7b6c7 route=/agent/v1/metrics error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=e6d5e0e6-0b68-4a55-9e5f-d70b0d76fbc6 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=d332deaf-4d5a-487d-818e-a9097f32c698 route=/agent/v1/metrics error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=cc18c16a-61ef-42ee-b754-34020328030d route=/agent/v1/metrics error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=cadd54a9-aef6-4a93-8d59-fbdde86f366d route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=be458b8c-d2a3-4ae0-9743-7665149aeb44 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=bcb7ad69-02f1-4b51-8051-d55d991710a6 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=b92a7330-46ca-4e92-9f58-f22877f31503 route=/agent/v1/metrics error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=b636376e-71c7-4f0d-b15a-63e5d6bf1ae2 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=b2d1d73a-26f6-49f3-9f6b-5c5d2d163f7b route=/agent/v1/metrics error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=b026f5a1-a333-4518-973a-b13daf4cd1ef route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=aa368cd7-9f34-4527-a590-042d23e42179 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=a9fe9ee9-66c3-4480-9f75-0453eb6f6507 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=a7f0e05c-3f09-41e2-9229-3370f980ac63 route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=a74a216e-1dd1-4ec2-9173-087feb1146ac route=/agent/v1/poll error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=95ba0615-bb4a-439c-9722-2beecd4469ee route=/agent/v1/metrics error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=8ecd8bff-58d2-4b8e-ada7-1003ab5240e5 route=/agent/v1/metrics error.category=context_canceled
      1 2026/11/06 12:20:35 ERROR request failed request_id=8c860226-d98c-4659-acf5-192529c48889 route=/agent/v1/poll error.category=context_canceled
```

### tmpfs-idle-2000

Window 480s; revision f06a5b8; ping round-trip min/avg/max = 0.346/0.559/1.686 ms; options map[agents:2000 appCPUs: appCpuset:0-3 dbCPUs: dbCpuset:4-7 dbRTT:300us dbTmpfs:true deployDuration:2m0s deploysPerDay:0 perWorkspace:5 serverPatched:false services:0 sharedBuffers:1GB warm:2m0s].

| Statement | calls/s | per agent/s | mean ms | exec ms/s | share of exec | WAL B/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AuthenticateAgent | 2229.2 | 1.115 | 0.010 | 22.7 | 43.2% | 7941 |
| ListTroubleshootingOffers | 1983.5 | 0.992 | 0.005 | 10.6 | 20.1% | 0 |
| poll command | 1983.5 | 0.992 | 0.003 | 5.9 | 11.3% | 0 |
| RecordAgentHeartbeat | 133.2 | 0.067 | 0.035 | 4.7 | 8.9% | 23689 |
| poll lease release | 79.2 | 0.040 | 0.036 | 2.9 | 5.5% | 12709 |
| poll lease take | 79.2 | 0.040 | 0.035 | 2.8 | 5.3% | 12695 |
| metrics insert | 33.4 | 0.017 | 0.054 | 1.8 | 3.4% | 42157 |
| metrics prune | 33.4 | 0.017 | 0.014 | 0.5 | 0.9% | 25693 |
| lockAgentCredential | 33.4 | 0.017 | 0.013 | 0.4 | 0.8% | 4240 |
| WITH locked_jobs AS ( SELECT id, args, attempt, attempted_at… | 1.2 | 0.001 | 0.035 | 0.0 | 0.1% | 282 |
| SELECT received_at,report FROM (SELECT received_at,report FR… | 1.7 | 0.001 | 0.020 | 0.0 | 0.1% | 0 |
| UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT i… | 0.3 | 0.000 | 0.092 | 0.0 | 0.1% | 134 |
| WITH job_input AS ( SELECT unnest($1::bigint[]) AS id, unnes… | 0.2 | 0.000 | 0.145 | 0.0 | 0.1% | 256 |
| DELETE FROM server_metrics WHERE (server_id,received_at) IN … | 0.0 | 0.000 | 1.632 | 0.0 | 0.1% | 976 |
| WITH raw_job_data AS ( SELECT unnest($1::bigint[]) AS id, un… | 0.2 | 0.000 | 0.089 | 0.0 | 0.0% | 343 |

WAL fsync: 217/s, mean 0.00 ms. Database: 1280685759 → 1286665919 bytes (12456 bytes/s). Throttled µs: DB 0, app 0. Generator CPU 0.044 cores. Load 4.85 6.01 5.65 → 6.11 5.09 5.20.

Largest table growth: server_metrics +5840896 (now 1257594880); river_job +65536 (now 311296); servers +0 (now 15310848); audit_events +0 (now 671744); auth_attempts +0 (now 450560); sessions +0 (now 204800); 

Deploys in window: started 0, finished 0, failed 0; delivery p50/p99 ms 0/0. Errors kept since the generator started, including setup, capped at 200: 200.
Oldest kept error: 22:26:14 poll: Get "http://fleet-app-f839d508:8080/agent/v1/poll": net/http: timeout awaiting response headers
