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
| tmpfs-idle-0500 | 500 | 1724 | 3.45 | 0.071 | 0.086 | 1196 | 85 | 5 / 0.25 | 116 / 120 | 756 / 378 | 41 | 15 | 4.1 / 16.4 | 0 | 23903 | 3.21 4.88 7.04 |
| tmpfs-idle-1000 | 1000 | 3351 | 3.35 | 0.149 | 0.185 | 2364 | 134 | 5 / 0.31 | 114 / 117 | 733 / 366 | 73 | 14 | 4.1 / 16.4 | 0 | 23656 | 3.87 5.17 5.47 |
| tmpfs-idle-1500-slow-db | 1500 | 4990 | 3.33 | 0.225 | 0.275 | 2984 | 185 | 5 / 0.59 | 114 / 119 | 728 / 363 | 98 | 14 | 8.2 / 524.3 | 0 | 20576 | 6.17 6.15 5.73 |
| tmpfs-idle-1500 | 1500 | 5127 | 3.42 | 0.175 | 0.216 | 2984 | 185 | 5 / 0.31 | 115 / 120 | 746 / 373 | 103 | 14 | 4.1 / 262.1 | 0 | 13874 | 3.98 5.98 6.32 |
| tmpfs-idle-2000-contended | 2000 | 6162 | 3.08 | 0.379 | 0.437 | 3371 | 269 | 5 / 0.97 | 114 / 122 | 677 / 336 | 150 | 14 | 262.1 / 4194.3 | 2604 | 9054 | 18.32 20.11 18.42 |
| tmpfs-idle-2000-no-tick | 2000 | 957 | 0.48 | 0.104 | 0.143 | 3378 | 243 | 5 / 0.34 | 114 / 117 | 152 / 55 | 158 | 14 | 4.1 / 16.4 | 0 | 24564 | 2.79 3.29 4.12 |
| tmpfs-idle-2000-pool12 | 2000 | 6811 | 3.41 | 0.372 | 0.426 | 3429 | 254 | 13 / 0.62 | 115 / 119 | 745 / 371 | 146 | 14 | 4.1 / 1048.6 | 0 | 18560 | 10.49 14.74 15.70 |
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
