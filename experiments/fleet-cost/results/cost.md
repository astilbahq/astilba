Burstable occupancy at speed scale 1, us-east-1 single-AZ:

| Run | Class | vCPUs used | Baseline | State | $/month |
| --- | --- | ---: | ---: | --- | ---: |
| tmpfs-idle-0100 | db.t4g.micro | 0.11 | 0.2 | holds, earns credits | 12 |
| tmpfs-idle-0100 | db.t4g.small | 0.11 | 0.4 | holds, earns credits | 23 |
| tmpfs-idle-0100 | db.t4g.medium | 0.11 | 0.4 | holds, earns credits | 47 |
| tmpfs-idle-0100 | db.t4g.large | 0.11 | 0.6 | holds, earns credits | 94 |
| tmpfs-idle-0100 | db.t4g.xlarge | 0.11 | 1.6 | holds, earns credits | 188 |
| tmpfs-idle-0100 | db.m8g.large | 0.07 | 0.0 | holds | 123 |
| tmpfs-idle-0100 | db.m8g.xlarge | 0.07 | 0.0 | holds | 245 |
| tmpfs-idle-0100 | db.m8gd.large | 0.07 | 0.0 | holds | 144 |
| tmpfs-idle-0100 | db.m8gd.xlarge | 0.07 | 0.0 | holds | 288 |
| tmpfs-idle-0100 | db.m6i.large | 0.11 | 0.0 | holds | 130 |
| tmpfs-idle-0100 | db.m6i.xlarge | 0.11 | 0.0 | holds | 260 |
| tmpfs-idle-0100 | db.m9g.large | 0.07 | 0.0 | holds | 134 |
| tmpfs-idle-0500 | db.t4g.micro | 0.25 | 0.2 | balance empty in 103 h, then +$3/mo | 14 |
| tmpfs-idle-0500 | db.t4g.small | 0.25 | 0.4 | holds, earns credits | 23 |
| tmpfs-idle-0500 | db.t4g.medium | 0.25 | 0.4 | holds, earns credits | 47 |
| tmpfs-idle-0500 | db.t4g.large | 0.25 | 0.6 | holds, earns credits | 94 |
| tmpfs-idle-0500 | db.t4g.xlarge | 0.25 | 1.6 | holds, earns credits | 188 |
| tmpfs-idle-0500 | db.m8g.large | 0.16 | 0.0 | holds | 123 |
| tmpfs-idle-0500 | db.m8g.xlarge | 0.16 | 0.0 | holds | 245 |
| tmpfs-idle-0500 | db.m8gd.large | 0.16 | 0.0 | holds | 144 |
| tmpfs-idle-0500 | db.m8gd.xlarge | 0.16 | 0.0 | holds | 288 |
| tmpfs-idle-0500 | db.m6i.large | 0.25 | 0.0 | holds | 130 |
| tmpfs-idle-0500 | db.m6i.xlarge | 0.25 | 0.0 | holds | 260 |
| tmpfs-idle-0500 | db.m9g.large | 0.16 | 0.0 | holds | 134 |
| tmpfs-idle-1000 | db.t4g.micro | 0.66 | 0.2 | too small | 12 |
| tmpfs-idle-1000 | db.t4g.small | 0.66 | 0.4 | balance empty in 36 h, then +$14/mo | 38 |
| tmpfs-idle-1000 | db.t4g.medium | 0.66 | 0.4 | balance empty in 36 h, then +$14/mo | 62 |
| tmpfs-idle-1000 | db.t4g.large | 0.66 | 0.6 | balance empty in 223 h, then +$4/mo | 98 |
| tmpfs-idle-1000 | db.t4g.xlarge | 0.66 | 1.6 | holds, earns credits | 188 |
| tmpfs-idle-1000 | db.m8g.large | 0.43 | 0.0 | holds | 123 |
| tmpfs-idle-1000 | db.m8g.xlarge | 0.43 | 0.0 | holds | 245 |
| tmpfs-idle-1000 | db.m8gd.large | 0.43 | 0.0 | holds | 144 |
| tmpfs-idle-1000 | db.m8gd.xlarge | 0.43 | 0.0 | holds | 288 |
| tmpfs-idle-1000 | db.m6i.large | 0.66 | 0.0 | holds | 130 |
| tmpfs-idle-1000 | db.m6i.xlarge | 0.66 | 0.0 | holds | 260 |
| tmpfs-idle-1000 | db.m9g.large | 0.43 | 0.0 | holds | 134 |
| tmpfs-active-1000 | db.t4g.micro | 2.14 | 0.2 | too small | 12 |
| tmpfs-active-1000 | db.t4g.small | 2.14 | 0.4 | too small | 23 |
| tmpfs-active-1000 | db.t4g.medium | 2.14 | 0.4 | too small | 47 |
| tmpfs-active-1000 | db.t4g.large | 2.14 | 0.6 | too small | 94 |
| tmpfs-active-1000 | db.t4g.xlarge | 2.14 | 1.6 | balance empty in 71 h, then +$30/mo | 218 |
| tmpfs-active-1000 | db.m8g.large | 1.38 | 0.0 | too small | 123 |
| tmpfs-active-1000 | db.m8g.xlarge | 1.38 | 0.0 | holds | 245 |
| tmpfs-active-1000 | db.m8gd.large | 1.38 | 0.0 | too small | 144 |
| tmpfs-active-1000 | db.m8gd.xlarge | 1.38 | 0.0 | holds | 288 |
| tmpfs-active-1000 | db.m6i.large | 2.14 | 0.0 | too small | 130 |
| tmpfs-active-1000 | db.m6i.xlarge | 2.14 | 0.0 | holds | 260 |
| tmpfs-active-1000 | db.m9g.large | 1.38 | 0.0 | too small | 134 |
| tmpfs-idle-1500 | db.t4g.micro | 0.61 | 0.2 | too small | 12 |
| tmpfs-idle-1500 | db.t4g.small | 0.61 | 0.4 | balance empty in 45 h, then +$12/mo | 35 |
| tmpfs-idle-1500 | db.t4g.medium | 0.61 | 0.4 | balance empty in 45 h, then +$12/mo | 59 |
| tmpfs-idle-1500 | db.t4g.large | 0.61 | 0.6 | balance empty in 1042 h, then +$1/mo | 95 |
| tmpfs-idle-1500 | db.t4g.xlarge | 0.61 | 1.6 | holds, earns credits | 188 |
| tmpfs-idle-1500 | db.m8g.large | 0.39 | 0.0 | holds | 123 |
| tmpfs-idle-1500 | db.m8g.xlarge | 0.39 | 0.0 | holds | 245 |
| tmpfs-idle-1500 | db.m8gd.large | 0.39 | 0.0 | holds | 144 |
| tmpfs-idle-1500 | db.m8gd.xlarge | 0.39 | 0.0 | holds | 288 |
| tmpfs-idle-1500 | db.m6i.large | 0.61 | 0.0 | holds | 130 |
| tmpfs-idle-1500 | db.m6i.xlarge | 0.61 | 0.0 | holds | 260 |
| tmpfs-idle-1500 | db.m9g.large | 0.39 | 0.0 | holds | 134 |
| tmpfs-idle-2000 | db.t4g.micro | 0.80 | 0.2 | too small | 12 |
| tmpfs-idle-2000 | db.t4g.small | 0.80 | 0.4 | too small | 23 |
| tmpfs-idle-2000 | db.t4g.medium | 0.80 | 0.4 | balance empty in 24 h, then +$22/mo | 69 |
| tmpfs-idle-2000 | db.t4g.large | 0.80 | 0.6 | balance empty in 71 h, then +$11/mo | 105 |
| tmpfs-idle-2000 | db.t4g.xlarge | 0.80 | 1.6 | holds, earns credits | 188 |
| tmpfs-idle-2000 | db.m8g.large | 0.52 | 0.0 | holds | 123 |
| tmpfs-idle-2000 | db.m8g.xlarge | 0.52 | 0.0 | holds | 245 |
| tmpfs-idle-2000 | db.m8gd.large | 0.52 | 0.0 | holds | 144 |
| tmpfs-idle-2000 | db.m8gd.xlarge | 0.52 | 0.0 | holds | 288 |
| tmpfs-idle-2000 | db.m6i.large | 0.80 | 0.0 | holds | 130 |
| tmpfs-idle-2000 | db.m6i.xlarge | 0.80 | 0.0 | holds | 260 |
| tmpfs-idle-2000 | db.m9g.large | 0.52 | 0.0 | holds | 134 |
| tmpfs-idle-2000-no-tick | db.t4g.micro | 0.37 | 0.2 | too small | 12 |
| tmpfs-idle-2000-no-tick | db.t4g.small | 0.37 | 0.4 | too small | 23 |
| tmpfs-idle-2000-no-tick | db.t4g.medium | 0.37 | 0.4 | holds, earns credits | 47 |
| tmpfs-idle-2000-no-tick | db.t4g.large | 0.37 | 0.6 | holds, earns credits | 94 |
| tmpfs-idle-2000-no-tick | db.t4g.xlarge | 0.37 | 1.6 | holds, earns credits | 188 |
| tmpfs-idle-2000-no-tick | db.m8g.large | 0.24 | 0.0 | holds | 123 |
| tmpfs-idle-2000-no-tick | db.m8g.xlarge | 0.24 | 0.0 | holds | 245 |
| tmpfs-idle-2000-no-tick | db.m8gd.large | 0.24 | 0.0 | holds | 144 |
| tmpfs-idle-2000-no-tick | db.m8gd.xlarge | 0.24 | 0.0 | holds | 288 |
| tmpfs-idle-2000-no-tick | db.m6i.large | 0.37 | 0.0 | holds | 130 |
| tmpfs-idle-2000-no-tick | db.m6i.xlarge | 0.37 | 0.0 | holds | 260 |
| tmpfs-idle-2000-no-tick | db.m9g.large | 0.24 | 0.0 | holds | 134 |

us-east-1, single-AZ, cheapest class that holds:

| Run | Database | DB $ | Storage + backup $ | Fargate | Task $ | ALB + IPv4 $ | Internet out $ | Logs $ | Total $/mo | $/server/mo | If task and DB cross AZs, +$/server/mo |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| tmpfs-idle-0100 | db.t4g.micro (0.11 vCPU; holds, earns credits) | 12 | 2.3 | 0.25 vCPU / 0.5 GB | 7 | 28 | 2.8 | 2.0 | 54 | 0.539 | 0.063 |
| tmpfs-idle-0500 | db.t4g.micro (0.25 vCPU; balance empty in 103 h, then +$3/mo) | 14 | 2.9 | 1 vCPU / 2 GB | 29 | 30 | 14.1 | 10.1 | 100 | 0.200 | 0.060 |
| tmpfs-idle-1000 | db.t4g.small (0.66 vCPU; balance empty in 36 h, then +$14/mo) | 38 | 4.3 | 2 vCPU / 4 GB | 58 | 32 | 27.8 | 19.9 | 180 | 0.180 | 0.058 |
| tmpfs-active-1000 | db.t4g.xlarge (2.14 vCPU; balance empty in 71 h, then +$30/mo) | 218 | 19.0 | 2 vCPU / 4 GB | 58 | 45 | 154.3 | 44.1 | 538 | 0.538 | 0.112 |
| tmpfs-idle-1500 | db.t4g.small (0.61 vCPU; balance empty in 45 h, then +$12/mo) | 35 | 6.5 | 2 vCPU / 4 GB | 58 | 35 | 42.5 | 30.3 | 207 | 0.138 | 0.059 |
| tmpfs-idle-2000 | db.t4g.medium (0.80 vCPU; balance empty in 24 h, then +$22/mo) | 69 | 9.2 | 2 vCPU / 4 GB | 58 | 37 | 58.6 | 40.9 | 273 | 0.137 | 0.059 |
| tmpfs-idle-2000-no-tick | db.t4g.medium (0.37 vCPU; holds, earns credits) | 47 | 9.6 | 1 vCPU / 2 GB | 29 | 37 | 55.5 | 39.8 | 218 | 0.109 | 0.011 |

us-east-1, Multi-AZ, cheapest class that holds:

| Run | Database | DB $ | Storage + backup $ | Fargate | Task $ | ALB + IPv4 $ | Internet out $ | Logs $ | Total $/mo | $/server/mo | If task and DB cross AZs, +$/server/mo |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| tmpfs-idle-0100 | db.t4g.micro (0.11 vCPU; holds, earns credits) | 23 | 4.6 | 0.25 vCPU / 0.5 GB | 7 | 28 | 2.8 | 2.0 | 68 | 0.679 | 0.063 |
| tmpfs-idle-0500 | db.t4g.micro (0.25 vCPU; balance empty in 103 h, then +$5/mo) | 28 | 5.2 | 1 vCPU / 2 GB | 29 | 30 | 14.1 | 10.1 | 117 | 0.233 | 0.060 |
| tmpfs-idle-1000 | db.t4g.small (0.66 vCPU; balance empty in 36 h, then +$29/mo) | 76 | 6.6 | 2 vCPU / 4 GB | 58 | 32 | 27.8 | 19.9 | 221 | 0.221 | 0.058 |
| tmpfs-active-1000 | db.t4g.xlarge (2.14 vCPU; balance empty in 71 h, then +$59/mo) | 437 | 21.3 | 2 vCPU / 4 GB | 58 | 45 | 154.3 | 44.1 | 759 | 0.759 | 0.112 |
| tmpfs-idle-1500 | db.t4g.small (0.61 vCPU; balance empty in 45 h, then +$23/mo) | 71 | 8.8 | 2 vCPU / 4 GB | 58 | 35 | 42.5 | 30.3 | 245 | 0.163 | 0.059 |
| tmpfs-idle-2000 | db.t4g.medium (0.80 vCPU; balance empty in 24 h, then +$44/mo) | 138 | 11.5 | 2 vCPU / 4 GB | 58 | 37 | 58.6 | 40.9 | 344 | 0.172 | 0.059 |
| tmpfs-idle-2000-no-tick | db.t4g.medium (0.37 vCPU; holds, earns credits) | 94 | 11.9 | 1 vCPU / 2 GB | 29 | 37 | 55.5 | 39.8 | 267 | 0.134 | 0.011 |

us-east-1, single-AZ, classes AWS lists for PostgreSQL 18:

| Run | Database | DB $ | Storage + backup $ | Fargate | Task $ | ALB + IPv4 $ | Internet out $ | Logs $ | Total $/mo | $/server/mo | If task and DB cross AZs, +$/server/mo |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| tmpfs-idle-0100 | db.m6i.large (0.11 vCPU; holds) | 130 | 2.3 | 0.25 vCPU / 0.5 GB | 7 | 28 | 2.8 | 2.0 | 172 | 1.722 | 0.063 |
| tmpfs-idle-0500 | db.m6i.large (0.25 vCPU; holds) | 130 | 2.9 | 1 vCPU / 2 GB | 29 | 30 | 14.1 | 10.1 | 216 | 0.432 | 0.060 |
| tmpfs-idle-1000 | db.m6i.large (0.66 vCPU; holds) | 130 | 4.3 | 2 vCPU / 4 GB | 58 | 32 | 27.8 | 19.9 | 272 | 0.272 | 0.058 |
| tmpfs-active-1000 | db.m6i.xlarge (2.14 vCPU; holds) | 260 | 19.0 | 2 vCPU / 4 GB | 58 | 45 | 154.3 | 44.1 | 580 | 0.580 | 0.112 |
| tmpfs-idle-1500 | db.m6i.large (0.61 vCPU; holds) | 130 | 6.5 | 2 vCPU / 4 GB | 58 | 35 | 42.5 | 30.3 | 302 | 0.201 | 0.059 |
| tmpfs-idle-2000 | db.m6i.large (0.80 vCPU; holds) | 130 | 9.2 | 2 vCPU / 4 GB | 58 | 37 | 58.6 | 40.9 | 334 | 0.167 | 0.059 |
| tmpfs-idle-2000-no-tick | db.m6i.large (0.37 vCPU; holds) | 130 | 9.6 | 1 vCPU / 2 GB | 29 | 37 | 55.5 | 39.8 | 301 | 0.150 | 0.011 |

us-east-1, Multi-AZ, classes AWS lists for PostgreSQL 18:

| Run | Database | DB $ | Storage + backup $ | Fargate | Task $ | ALB + IPv4 $ | Internet out $ | Logs $ | Total $/mo | $/server/mo | If task and DB cross AZs, +$/server/mo |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| tmpfs-idle-0100 | db.m6i.large (0.11 vCPU; holds) | 260 | 4.6 | 0.25 vCPU / 0.5 GB | 7 | 28 | 2.8 | 2.0 | 304 | 3.044 | 0.063 |
| tmpfs-idle-0500 | db.m6i.large (0.25 vCPU; holds) | 260 | 5.2 | 1 vCPU / 2 GB | 29 | 30 | 14.1 | 10.1 | 348 | 0.696 | 0.060 |
| tmpfs-idle-1000 | db.m6i.large (0.66 vCPU; holds) | 260 | 6.6 | 2 vCPU / 4 GB | 58 | 32 | 27.8 | 19.9 | 404 | 0.404 | 0.058 |
| tmpfs-active-1000 | db.m6i.xlarge (2.14 vCPU; holds) | 520 | 21.3 | 2 vCPU / 4 GB | 58 | 45 | 154.3 | 44.1 | 843 | 0.843 | 0.112 |
| tmpfs-idle-1500 | db.m6i.large (0.61 vCPU; holds) | 260 | 8.8 | 2 vCPU / 4 GB | 58 | 35 | 42.5 | 30.3 | 434 | 0.289 | 0.059 |
| tmpfs-idle-2000 | db.m6i.large (0.80 vCPU; holds) | 260 | 11.5 | 2 vCPU / 4 GB | 58 | 37 | 58.6 | 40.9 | 466 | 0.233 | 0.059 |
| tmpfs-idle-2000-no-tick | db.m6i.large (0.37 vCPU; holds) | 260 | 11.9 | 1 vCPU / 2 GB | 29 | 37 | 55.5 | 39.8 | 433 | 0.217 | 0.011 |

eu-west-1, single-AZ, cheapest class that holds:

| Run | Database | DB $ | Storage + backup $ | Fargate | Task $ | ALB + IPv4 $ | Internet out $ | Logs $ | Total $/mo | $/server/mo | If task and DB cross AZs, +$/server/mo |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| tmpfs-idle-0100 | db.t4g.micro (0.11 vCPU; holds, earns credits) | 12 | 2.5 | 0.25 vCPU / 0.5 GB | 7 | 30 | 2.8 | 2.3 | 57 | 0.571 | 0.063 |
| tmpfs-idle-0500 | db.t4g.micro (0.25 vCPU; balance empty in 103 h, then +$3/mo) | 15 | 3.1 | 1 vCPU / 2 GB | 29 | 32 | 14.1 | 11.5 | 104 | 0.209 | 0.060 |
| tmpfs-idle-1000 | db.t4g.small (0.66 vCPU; balance empty in 36 h, then +$14/mo) | 40 | 4.6 | 2 vCPU / 4 GB | 58 | 34 | 27.8 | 22.5 | 187 | 0.187 | 0.058 |
| tmpfs-active-1000 | db.t4g.xlarge (2.14 vCPU; balance empty in 71 h, then +$30/mo) | 232 | 19.2 | 2 vCPU / 4 GB | 58 | 47 | 154.3 | 49.9 | 560 | 0.560 | 0.112 |
| tmpfs-idle-1500 | db.t4g.small (0.61 vCPU; balance empty in 45 h, then +$12/mo) | 37 | 6.8 | 2 vCPU / 4 GB | 58 | 37 | 42.5 | 34.3 | 215 | 0.143 | 0.059 |
| tmpfs-idle-2000 | db.t4g.medium (0.80 vCPU; balance empty in 24 h, then +$22/mo) | 72 | 9.4 | 2 vCPU / 4 GB | 58 | 39 | 58.6 | 46.3 | 284 | 0.142 | 0.059 |
| tmpfs-idle-2000-no-tick | db.t4g.medium (0.37 vCPU; holds, earns credits) | 50 | 9.8 | 1 vCPU / 2 GB | 29 | 39 | 55.5 | 45.0 | 229 | 0.114 | 0.011 |

eu-west-1, Multi-AZ, cheapest class that holds:

| Run | Database | DB $ | Storage + backup $ | Fargate | Task $ | ALB + IPv4 $ | Internet out $ | Logs $ | Total $/mo | $/server/mo | If task and DB cross AZs, +$/server/mo |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| tmpfs-idle-0100 | db.t4g.micro (0.11 vCPU; holds, earns credits) | 26 | 5.1 | 0.25 vCPU / 0.5 GB | 7 | 30 | 2.8 | 2.3 | 73 | 0.728 | 0.063 |
| tmpfs-idle-0500 | db.t4g.micro (0.25 vCPU; balance empty in 103 h, then +$5/mo) | 31 | 5.6 | 1 vCPU / 2 GB | 29 | 32 | 14.1 | 11.5 | 123 | 0.245 | 0.060 |
| tmpfs-idle-1000 | db.t4g.small (0.66 vCPU; balance empty in 36 h, then +$29/mo) | 79 | 7.1 | 2 vCPU / 4 GB | 58 | 34 | 27.8 | 22.5 | 229 | 0.229 | 0.058 |
| tmpfs-active-1000 | db.t4g.xlarge (2.14 vCPU; balance empty in 71 h, then +$59/mo) | 464 | 21.8 | 2 vCPU / 4 GB | 58 | 47 | 154.3 | 49.9 | 795 | 0.795 | 0.112 |
| tmpfs-idle-1500 | db.t4g.small (0.61 vCPU; balance empty in 45 h, then +$23/mo) | 74 | 9.3 | 2 vCPU / 4 GB | 58 | 37 | 42.5 | 34.3 | 254 | 0.170 | 0.059 |
| tmpfs-idle-2000 | db.t4g.medium (0.80 vCPU; balance empty in 24 h, then +$44/mo) | 145 | 12.0 | 2 vCPU / 4 GB | 58 | 39 | 58.6 | 46.3 | 359 | 0.179 | 0.059 |
| tmpfs-idle-2000-no-tick | db.t4g.medium (0.37 vCPU; holds, earns credits) | 101 | 12.4 | 1 vCPU / 2 GB | 29 | 39 | 55.5 | 45.0 | 282 | 0.141 | 0.011 |

eu-west-1, single-AZ, classes AWS lists for PostgreSQL 18:

| Run | Database | DB $ | Storage + backup $ | Fargate | Task $ | ALB + IPv4 $ | Internet out $ | Logs $ | Total $/mo | $/server/mo | If task and DB cross AZs, +$/server/mo |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| tmpfs-idle-0100 | db.m6i.large (0.11 vCPU; holds) | 144 | 2.5 | 0.25 vCPU / 0.5 GB | 7 | 30 | 2.8 | 2.3 | 189 | 1.885 | 0.063 |
| tmpfs-idle-0500 | db.m6i.large (0.25 vCPU; holds) | 144 | 3.1 | 1 vCPU / 2 GB | 29 | 32 | 14.1 | 11.5 | 233 | 0.466 | 0.060 |
| tmpfs-idle-1000 | db.m6i.large (0.66 vCPU; holds) | 144 | 4.6 | 2 vCPU / 4 GB | 58 | 34 | 27.8 | 22.5 | 291 | 0.291 | 0.058 |
| tmpfs-active-1000 | db.m6i.xlarge (2.14 vCPU; holds) | 288 | 19.2 | 2 vCPU / 4 GB | 58 | 47 | 154.3 | 49.9 | 616 | 0.616 | 0.112 |
| tmpfs-idle-1500 | db.m6i.large (0.61 vCPU; holds) | 144 | 6.8 | 2 vCPU / 4 GB | 58 | 37 | 42.5 | 34.3 | 322 | 0.215 | 0.059 |
| tmpfs-idle-2000 | db.m6i.large (0.80 vCPU; holds) | 144 | 9.4 | 2 vCPU / 4 GB | 58 | 39 | 58.6 | 46.3 | 355 | 0.178 | 0.059 |
| tmpfs-idle-2000-no-tick | db.m6i.large (0.37 vCPU; holds) | 144 | 9.8 | 1 vCPU / 2 GB | 29 | 39 | 55.5 | 45.0 | 322 | 0.161 | 0.011 |

eu-west-1, Multi-AZ, classes AWS lists for PostgreSQL 18:

| Run | Database | DB $ | Storage + backup $ | Fargate | Task $ | ALB + IPv4 $ | Internet out $ | Logs $ | Total $/mo | $/server/mo | If task and DB cross AZs, +$/server/mo |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| tmpfs-idle-0100 | db.m6i.large (0.11 vCPU; holds) | 288 | 5.1 | 0.25 vCPU / 0.5 GB | 7 | 30 | 2.8 | 2.3 | 335 | 3.349 | 0.063 |
| tmpfs-idle-0500 | db.m6i.large (0.25 vCPU; holds) | 288 | 5.6 | 1 vCPU / 2 GB | 29 | 32 | 14.1 | 11.5 | 380 | 0.759 | 0.060 |
| tmpfs-idle-1000 | db.m6i.large (0.66 vCPU; holds) | 288 | 7.1 | 2 vCPU / 4 GB | 58 | 34 | 27.8 | 22.5 | 437 | 0.437 | 0.058 |
| tmpfs-active-1000 | db.m6i.xlarge (2.14 vCPU; holds) | 575 | 21.8 | 2 vCPU / 4 GB | 58 | 47 | 154.3 | 49.9 | 906 | 0.906 | 0.112 |
| tmpfs-idle-1500 | db.m6i.large (0.61 vCPU; holds) | 288 | 9.3 | 2 vCPU / 4 GB | 58 | 37 | 42.5 | 34.3 | 468 | 0.312 | 0.059 |
| tmpfs-idle-2000 | db.m6i.large (0.80 vCPU; holds) | 288 | 12.0 | 2 vCPU / 4 GB | 58 | 39 | 58.6 | 46.3 | 502 | 0.251 | 0.059 |
| tmpfs-idle-2000-no-tick | db.m6i.large (0.37 vCPU; holds) | 288 | 12.4 | 1 vCPU / 2 GB | 29 | 39 | 55.5 | 45.0 | 468 | 0.234 | 0.011 |
