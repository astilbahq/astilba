package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	postgresImage       = "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e"
	ownerLabel          = "dev.astilba.fleet-capacity"
	ownerEmail          = "owner@fleet.example.test"
	publicOrigin        = "https://fleet.example.test"
	statsURL            = "http://127.0.0.1:9100/stats"
	defaultServerSource = "../../services/platform"
)

type options struct {
	context, out, name, platform, appCpuset, dbCpuset, genCpuset, rtt, dbCPUs, appCPUs, serverPackage, sharedBuffers string
	agents, services, perWorkspace                                                                                   int
	deploysPerDay                                                                                                    float64
	warm, window, sample, deployDuration                                                                             time.Duration
	calibrate, keep, tmpfs                                                                                           bool
}

type docker struct{ context string }

func (d docker) run(ctx context.Context, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--context", d.context}, args...)...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %.500s", strings.Join(args[:min(len(args), 3)], " "), err, errOut.String())
	}
	return strings.TrimSpace(out.String()), nil
}

type counters struct {
	CPUMicros                         int64 `json:"cpuMicros"`
	ThrottledMicros                   int64 `json:"throttledMicros"`
	MemoryCurrent, MemoryPeak         int64
	MemoryAnon, MemoryFile, MemoryShm int64
	NetRX, NetTX                      int64
}

func (d docker) counters(ctx context.Context, name string) (counters, error) {
	raw, err := d.run(ctx, "", "exec", name, "sh", "-c", `cat /sys/fs/cgroup/cpu.stat; echo memory_current $(cat /sys/fs/cgroup/memory.current); echo memory_peak $(cat /sys/fs/cgroup/memory.peak); cat /sys/fs/cgroup/memory.stat | grep -E '^(anon|file|shmem) '; grep eth0 /proc/net/dev`)
	var c counters
	if err != nil {
		return c, err
	}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(strings.ReplaceAll(line, ":", " "))
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "usage_usec":
			c.CPUMicros = v
		case "throttled_usec":
			c.ThrottledMicros = v
		case "memory_current":
			c.MemoryCurrent = v
		case "memory_peak":
			c.MemoryPeak = v
		case "anon":
			c.MemoryAnon = v
		case "file":
			c.MemoryFile = v
		case "shmem":
			c.MemoryShm = v
		case "eth0":
			tx, _ := strconv.ParseInt(f[9], 10, 64)
			c.NetRX, c.NetTX = v, tx
		}
	}
	return c, nil
}

func (d docker) sql(ctx context.Context, db, query string) (string, error) {
	return d.run(ctx, "", "exec", "-e", "PGOPTIONS=-c pg_stat_statements.track=none", names.db, "psql", "-U", "astilba", "-d", db, "-AtX", "-v", "ON_ERROR_STOP=1", "-c", query)
}

var names struct{ net, db, app, gen string }

const snapshotSQL = `SELECT json_build_object(
 'databaseBytes', pg_database_size(current_database()),
 'walLSN', pg_current_wal_lsn()-'0/0'::pg_lsn,
 'wal', (SELECT row_to_json(w) FROM pg_stat_wal w),
 'database', (SELECT row_to_json(d) FROM (SELECT xact_commit,xact_rollback,blks_read,blks_hit,tup_returned,tup_fetched,tup_inserted,tup_updated,tup_deleted,numbackends FROM pg_stat_database WHERE datname=current_database()) d),
 'checkpointer', (SELECT row_to_json(c) FROM pg_stat_checkpointer c),
 'walFsync', (SELECT json_build_object('fsyncs',sum(fsyncs),'millis',sum(fsync_time)) FROM pg_stat_io WHERE object='wal'),
 'servers', (SELECT count(*) FROM servers WHERE credential_hash IS NOT NULL),
 'tables', (SELECT json_agg(t) FROM (SELECT c.relname,pg_total_relation_size(c.oid) AS total_bytes,pg_relation_size(c.oid) AS heap_bytes,s.n_live_tup,s.n_dead_tup,s.n_tup_ins,s.n_tup_upd,s.n_tup_hot_upd,s.n_tup_del,s.autovacuum_count FROM pg_stat_user_tables s JOIN pg_class c ON c.oid=s.relid ORDER BY 2 DESC) t))`

const statementsSQL = `SELECT COALESCE(json_agg(s),'[]') FROM (SELECT queryid,left(regexp_replace(query,'\s+',' ','g'),400) AS query,calls,total_exec_time,mean_exec_time,max_exec_time,rows,shared_blks_hit,shared_blks_read,shared_blks_dirtied,shared_blks_written,wal_bytes,wal_records FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) ORDER BY total_exec_time DESC) s`

const activitySQL = `SELECT json_build_object('clients',count(*) FILTER (WHERE backend_type='client backend' AND pid<>pg_backend_pid()),'active',count(*) FILTER (WHERE backend_type='client backend' AND state='active' AND pid<>pg_backend_pid()),'autovacuum',count(*) FILTER (WHERE backend_type='autovacuum worker')) FROM pg_stat_activity`

type sample struct {
	At       time.Time       `json:"at"`
	DB       counters        `json:"db"`
	App      counters        `json:"app"`
	Activity json.RawMessage `json:"activity"`
}

type result struct {
	Name        string          `json:"name"`
	Options     map[string]any  `json:"options"`
	StartedAt   time.Time       `json:"startedAt"`
	Revision    string          `json:"revision"`
	Calibration map[string]any  `json:"calibration,omitempty"`
	Seconds     float64         `json:"windowSeconds"`
	Before      json.RawMessage `json:"before"`
	After       json.RawMessage `json:"after"`
	DBBefore    counters        `json:"dbBefore"`
	DBAfter     counters        `json:"dbAfter"`
	AppBefore   counters        `json:"appBefore"`
	AppAfter    counters        `json:"appAfter"`
	GenBefore   counters        `json:"generatorBefore"`
	GenAfter    counters        `json:"generatorAfter"`
	AppLogBytes int64           `json:"applicationLogBytesInWindow"`
	Ping        string          `json:"appToDatabasePing"`
	Statements  json.RawMessage `json:"statements"`
	StatsBefore json.RawMessage `json:"agentStatsBefore"`
	StatsAfter  json.RawMessage `json:"agentStatsAfter"`
	Samples     []sample        `json:"samples"`
	LoadBefore  string          `json:"hostLoadBefore"`
	LoadAfter   string          `json:"hostLoadAfter"`
	AppErrors   string          `json:"applicationErrorLines,omitempty"`
}

func runMeasure(args []string) error {
	var o options
	f := flag.NewFlagSet("measure", flag.ContinueOnError)
	f.StringVar(&o.context, "docker-context", "", "explicitly disposable Docker context (required)")
	f.StringVar(&o.out, "out", "", "output directory (required)")
	f.StringVar(&o.name, "name", "", "run name (required)")
	f.StringVar(&o.platform, "goarch", "arm64", "architecture of the Docker host")
	f.IntVar(&o.agents, "agents", 100, "synthetic agents")
	f.IntVar(&o.services, "services", 0, "deployed services per server")
	f.IntVar(&o.perWorkspace, "per-workspace", 5, "servers per workspace")
	f.Float64Var(&o.deploysPerDay, "deploys-per-day", 0, "deployments per server per day")
	f.DurationVar(&o.deployDuration, "deploy-duration", 2*time.Minute, "time a deployment stays in progress")
	f.DurationVar(&o.warm, "warm", 3*time.Minute, "time between the last agent starting and the window")
	f.DurationVar(&o.window, "window", 10*time.Minute, "measurement window")
	f.DurationVar(&o.sample, "sample", 15*time.Second, "time series interval")
	f.StringVar(&o.appCpuset, "app-cpuset", "0-3", "application CPUs; the database pool has max(4, this count) connections, and Fargate reports 2 or 4")
	f.StringVar(&o.dbCpuset, "db-cpuset", "4-7", "PostgreSQL CPUs")
	f.StringVar(&o.genCpuset, "generator-cpuset", "8-11", "load generator CPUs")
	f.StringVar(&o.rtt, "db-rtt", "300us", "netem delay on PostgreSQL's replies, empty for none; each run records the resulting ping")
	f.StringVar(&o.dbCPUs, "db-cpus", "", "docker --cpus for PostgreSQL")
	f.StringVar(&o.sharedBuffers, "shared-buffers", "1GB", "PostgreSQL shared_buffers")
	f.StringVar(&o.appCPUs, "app-cpus", "", "docker --cpus for the application")
	f.StringVar(&o.serverPackage, "server-source", defaultServerSource, "platform module to build; override only for a clearly labelled counterfactual")
	f.BoolVar(&o.calibrate, "calibrate", true, "run pgbench calibration before the application starts")
	f.BoolVar(&o.keep, "keep", false, "leave containers running for inspection")
	f.BoolVar(&o.tmpfs, "db-tmpfs", false, "keep PostgreSQL's files in memory with a 1 ms commit delay, removing this host's disk from the commit path")
	if err := f.Parse(args); err != nil {
		return err
	}
	if o.context == "" || o.out == "" || o.name == "" {
		return errors.New("--docker-context, --out and --name are required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d := docker{o.context}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	id := hex.EncodeToString(suffix)
	names.net, names.db, names.app, names.gen = "fleet-"+id, "fleet-db-"+id, "fleet-app-"+id, "fleet-gen-"+id
	defer func() {
		if !o.keep {
			cleanup(d, id)
		}
	}()
	work, err := os.MkdirTemp("", "fleet-capacity-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	if err = build(ctx, o, work); err != nil {
		return err
	}
	res := &result{Name: o.name, StartedAt: time.Now().UTC(), Options: map[string]any{"agents": o.agents, "services": o.services, "perWorkspace": o.perWorkspace, "deploysPerDay": o.deploysPerDay, "deployDuration": o.deployDuration.String(), "appCpuset": o.appCpuset, "dbCpuset": o.dbCpuset, "dbRTT": o.rtt, "dbCPUs": o.dbCPUs, "appCPUs": o.appCPUs, "serverPatched": o.serverPackage != defaultServerSource, "warm": o.warm.String(), "dbTmpfs": o.tmpfs, "sharedBuffers": o.sharedBuffers}}
	if rev, e := exec.Command("git", "describe", "--always", "--dirty").Output(); e == nil {
		res.Revision = strings.TrimSpace(string(rev))
	}
	label := ownerLabel + "=" + id
	if _, err = d.run(ctx, "", "network", "create", "--label", label, names.net); err != nil {
		return err
	}
	password := hex.EncodeToString(suffix) + "-fleet-capacity-disposable"
	dbArgs := []string{"run", "-d", "--name", names.db, "--label", label, "--network", names.net, "-e", "POSTGRES_USER=astilba", "-e", "POSTGRES_PASSWORD=" + password, "-e", "POSTGRES_DB=astilba", "--tmpfs", "/tmp", "--cpuset-cpus", o.dbCpuset}
	if o.dbCPUs != "" {
		dbArgs = append(dbArgs, "--cpus", o.dbCPUs)
	}
	if o.tmpfs {
		dbArgs = append(dbArgs, "--tmpfs", "/var/lib/postgresql:size=8g")
	}
	dbArgs = append(dbArgs, postgresImage, "postgres", "-c", "shared_preload_libraries=pg_stat_statements", "-c", "pg_stat_statements.track=top", "-c", "pg_stat_statements.max=5000", "-c", "track_io_timing=on", "-c", "track_wal_io_timing=on", "-c", "shared_buffers="+o.sharedBuffers, "-c", "max_connections=200", "-c", "log_min_messages=warning")
	if o.tmpfs {
		dbArgs = append(dbArgs, "-c", "commit_delay=1000", "-c", "commit_siblings=0")
	}
	if _, err = d.run(ctx, "", dbArgs...); err != nil {
		return err
	}
	for i := 0; ; i++ {
		if _, e := d.sql(ctx, "astilba", "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"); e == nil {
			break
		} else if i > 120 {
			return e
		}
		time.Sleep(time.Second)
	}
	if o.rtt != "" {
		if err = addLatency(ctx, d, o.rtt); err != nil {
			return err
		}
	}
	if o.calibrate {
		if res.Calibration, err = calibrate(ctx, d); err != nil {
			return err
		}
	}
	base := []string{"--label", label, "--network", names.net, "--ulimit", "nofile=200000:200000", "--entrypoint", "/bin/sh"}
	appArgs := append([]string{"run", "-d", "--name", names.app, "--cpuset-cpus", o.appCpuset}, base...)
	if o.appCPUs != "" {
		appArgs = append(appArgs, "--cpus", o.appCPUs)
	}
	if _, err = d.run(ctx, "", append(appArgs, postgresImage, "-c", "exec sleep 2147483647")...); err != nil {
		return err
	}
	if _, err = d.run(ctx, "", append(append([]string{"run", "-d", "--name", names.gen, "--cpuset-cpus", o.genCpuset}, base...), postgresImage, "-c", "exec sleep 2147483647")...); err != nil {
		return err
	}
	if _, err = d.run(ctx, "", "cp", filepath.Join(work, "astilba"), names.app+":/usr/local/bin/astilba"); err != nil {
		return err
	}
	if _, err = d.run(ctx, "", "cp", filepath.Join(work, "fleet-capacity"), names.gen+":/usr/local/bin/fleet-capacity"); err != nil {
		return err
	}
	dsn := fmt.Sprintf("postgres://astilba:%s@%s:5432/astilba?sslmode=disable", password, names.db)
	env := []string{"-e", "ASTILBA_DATABASE_URL=" + dsn, "-e", "ASTILBA_KEYRING=/root/keyring.json", "-e", "ASTILBA_ORIGIN=" + publicOrigin, "-e", "ASTILBA_LISTEN=0.0.0.0:8080", "-e", "ASTILBA_TRUSTED_PROXIES=0.0.0.0/0"}
	exec1 := func(stdin string, args ...string) error {
		_, e := d.run(ctx, stdin, append(append(append([]string{"exec", "-i"}, env...), names.app, "astilba"), args...)...)
		return e
	}
	if err = exec1("", "keygen", "--out", "/root/keyring.json"); err != nil {
		return err
	}
	if err = exec1("", "migrate"); err != nil {
		return err
	}
	ownerPassword := "fleet-" + hex.EncodeToString(suffix) + "-owner-password"
	if err = exec1(ownerPassword+"\n", "bootstrap", "--email", ownerEmail, "--name", "Fleet Owner", "--workspace", "Personal"); err != nil {
		return err
	}
	if _, err = d.run(ctx, "", append(append(append([]string{"exec", "-d"}, env...), names.app), "sh", "-c", "exec astilba serve >/tmp/app.log 2>&1")...); err != nil {
		return err
	}
	if err = waitFor(ctx, d, names.app, "wget -q -O /dev/null http://127.0.0.1:8080/api/v1/session", 60); err != nil {
		return fmt.Errorf("application did not start: %w", err)
	}
	res.Ping, _ = d.run(ctx, "", "exec", names.app, "sh", "-c", "ping -c 100 -i 0.02 -q "+names.db+" | tail -1")
	genCmd := fmt.Sprintf("exec fleet-capacity agents --origin http://%s:8080 --public-origin %s --email %s --agents %d --services %d --per-workspace %d --deploys-per-day %g --deploy-duration %s >/tmp/gen.log 2>&1", names.app, publicOrigin, ownerEmail, o.agents, o.services, o.perWorkspace, o.deploysPerDay, o.deployDuration)
	if _, err = d.run(ctx, "", "exec", "-d", "-e", "FLEET_OWNER_PASSWORD="+ownerPassword, names.gen, "sh", "-c", genCmd); err != nil {
		return err
	}
	log.Printf("%s: waiting for %d agents to be provisioned", o.name, o.agents)
	for {
		raw, e := d.run(ctx, "", "exec", names.gen, "wget", "-q", "-O", "-", statsURL)
		if e == nil {
			var s struct {
				Ready bool `json:"ready"`
			}
			if json.Unmarshal([]byte(raw), &s) == nil && s.Ready {
				break
			}
		}
		// Checked only after the sleep: a process just started by exec -d may not be named yet.
		if !wait(ctx, 5*time.Second) {
			return ctx.Err()
		}
		if alive, _ := d.run(ctx, "", "exec", names.gen, "sh", "-c", "pidof fleet-capacity >/dev/null && echo up || tail -20 /tmp/gen.log"); alive != "up" {
			return fmt.Errorf("generator stopped: %s", alive)
		}
	}
	if o.agents > 0 {
		// Agents start over a one-minute ramp, then report metrics within another minute.
		if !wait(ctx, 2*time.Minute+10*time.Second) {
			return ctx.Err()
		}
		if err = seedMetricHistory(ctx, d); err != nil {
			return err
		}
	}
	log.Printf("%s: warming for %s", o.name, o.warm)
	if !wait(ctx, o.warm) {
		return ctx.Err()
	}
	if err = measureWindow(ctx, d, o, res); err != nil {
		return err
	}
	res.AppErrors, _ = d.run(ctx, "", "exec", names.app, "sh", "-c", `grep -iE '"level":"(WARN|ERROR)"|level=(WARN|ERROR)' /tmp/app.log | cut -c1-240 | sort | uniq -c | sort -rn | head -20`)
	if err = os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(res, "", " ")
	path := filepath.Join(o.out, o.name+".json")
	if err = os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}
	log.Printf("%s: wrote %s", o.name, path)
	return nil
}

func build(ctx context.Context, o options, work string) error {
	env := append(os.Environ(), "GOOS=linux", "GOARCH="+o.platform, "CGO_ENABLED=0")
	server := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(work, "astilba"), "./cmd/server")
	server.Dir, server.Env = o.serverPackage, env
	if out, err := server.CombinedOutput(); err != nil {
		return fmt.Errorf("build server: %w: %s", err, out)
	}
	self := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(work, "fleet-capacity"), ".")
	self.Env = env
	if out, err := self.CombinedOutput(); err != nil {
		return fmt.Errorf("build generator: %w: %s", err, out)
	}
	return nil
}

func waitFor(ctx context.Context, d docker, name, script string, attempts int) error {
	var err error
	for range attempts {
		if _, err = d.run(ctx, "", "exec", name, "sh", "-c", script); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return err
}

// addLatency delays PostgreSQL's replies inside its own network namespace, using the
// VM's tc. A managed database is never on the same host: pool occupancy depends on it.
func addLatency(ctx context.Context, d docker, rtt string) error {
	pid, err := d.run(ctx, "", "inspect", "-f", "{{.State.Pid}}", names.db)
	if err != nil {
		return err
	}
	script := fmt.Sprintf("nsenter --mount=/proc/1/ns/mnt -- modprobe sch_netem; nsenter --mount=/proc/1/ns/mnt --net=/proc/%s/ns/net -- /usr/sbin/tc qdisc add dev eth0 root netem delay %s limit 100000", pid, rtt)
	if _, err = d.run(ctx, "", "run", "--rm", "--privileged", "--pid=host", "--label", ownerLabel+"="+strings.TrimPrefix(names.net, "fleet-"), "--entrypoint", "/bin/sh", postgresImage, "-c", script); err != nil {
		return fmt.Errorf("add latency (needs tc in the Docker VM): %w", err)
	}
	return nil
}

// calibrate records single-core PostgreSQL speed on this host and its scheduling noise,
// so the raw CPU figures can be related to another machine by running the same command there.
func calibrate(ctx context.Context, d docker) (map[string]any, error) {
	out := map[string]any{}
	if _, err := d.sql(ctx, "astilba", "CREATE DATABASE calibration"); err != nil {
		return nil, err
	}
	if _, err := d.run(ctx, "", "exec", names.db, "pgbench", "-U", "astilba", "-h", "/var/run/postgresql", "-i", "-q", "-s", "10", "calibration"); err != nil {
		return nil, err
	}
	for name, flags := range map[string][]string{"selectOnlyTPS": {"-S"}, "tpcbTPS": {}} {
		args := append([]string{"exec", names.db, "pgbench", "-U", "astilba", "-h", "/var/run/postgresql", "-c", "1", "-j", "1", "-T", "20", "-n"}, flags...)
		raw, err := d.run(ctx, "", append(args, "calibration")...)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(raw, "\n") {
			if strings.HasPrefix(line, "tps = ") {
				v, _ := strconv.ParseFloat(strings.Fields(line)[2], 64)
				out[name] = v
			}
		}
	}
	if _, err := d.sql(ctx, "astilba", "DROP DATABASE calibration"); err != nil {
		return nil, err
	}
	return out, nil
}

// hostLoad reads the machine running this command, which is the Docker host only when
// the Docker VM is local. The guest cannot see other VMs competing for the same cores.
func hostLoad() string {
	raw, err := exec.Command("uptime").Output()
	if err != nil {
		return ""
	}
	_, load, _ := strings.Cut(string(raw), "load average")
	return strings.TrimSpace(strings.NewReplacer("s:", "", ":", "", ",", "").Replace(load))
}

func measureWindow(ctx context.Context, d docker, o options, res *result) error {
	var err error
	if _, err = d.sql(ctx, "astilba", "SELECT pg_stat_statements_reset()"); err != nil {
		return err
	}
	res.LoadBefore = hostLoad()
	logBytes := func() int64 {
		raw, _ := d.run(ctx, "", "exec", names.app, "sh", "-c", "wc -c </tmp/app.log")
		v, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		return v
	}
	logsBefore := logBytes()
	begin := time.Now()
	snapshot := func() (json.RawMessage, error) {
		s, e := d.sql(ctx, "astilba", snapshotSQL)
		return json.RawMessage(s), e
	}
	if res.Before, err = snapshot(); err != nil {
		return err
	}
	if res.DBBefore, err = d.counters(ctx, names.db); err != nil {
		return err
	}
	if res.AppBefore, err = d.counters(ctx, names.app); err != nil {
		return err
	}
	if res.GenBefore, err = d.counters(ctx, names.gen); err != nil {
		return err
	}
	raw, err := d.run(ctx, "", "exec", names.gen, "wget", "-q", "-O", "-", statsURL)
	if err != nil {
		return err
	}
	res.StatsBefore = json.RawMessage(raw)
	log.Printf("%s: window %s", o.name, o.window)
	deadline := begin.Add(o.window)
	for time.Now().Before(deadline) {
		if !wait(ctx, min(o.sample, time.Until(deadline))) {
			return ctx.Err()
		}
		s := sample{At: time.Now().UTC()}
		s.DB, _ = d.counters(ctx, names.db)
		s.App, _ = d.counters(ctx, names.app)
		activity, e := d.sql(ctx, "astilba", activitySQL)
		if e == nil {
			s.Activity = json.RawMessage(activity)
		}
		res.Samples = append(res.Samples, s)
	}
	res.Seconds = time.Since(begin).Seconds()
	res.AppLogBytes = logBytes() - logsBefore
	if res.DBAfter, err = d.counters(ctx, names.db); err != nil {
		return err
	}
	if res.AppAfter, err = d.counters(ctx, names.app); err != nil {
		return err
	}
	if res.GenAfter, err = d.counters(ctx, names.gen); err != nil {
		return err
	}
	if raw, err = d.run(ctx, "", "exec", names.gen, "wget", "-q", "-O", "-", statsURL); err != nil {
		return err
	}
	res.StatsAfter = json.RawMessage(raw)
	statements, err := d.sql(ctx, "astilba", statementsSQL)
	if err != nil {
		return err
	}
	res.Statements = json.RawMessage(statements)
	if res.After, err = snapshot(); err != nil {
		return err
	}
	res.LoadAfter = hostLoad()
	return nil
}

// seedMetricHistory fills the 24-hour retention with each server's own first report,
// so the window sees the real steady state: one insert and one prune per server-minute
// against a full table. A short run from empty would otherwise never delete anything.
func seedMetricHistory(ctx context.Context, d docker) error {
	if _, err := d.sql(ctx, "astilba", `INSERT INTO server_metrics(workspace_id,server_id,received_at,report)
 SELECT m.workspace_id,m.server_id,date_trunc('minute',now())-make_interval(mins=>g),m.report
 FROM (SELECT DISTINCT ON (server_id) * FROM server_metrics ORDER BY server_id,received_at DESC) m CROSS JOIN generate_series(1,1439) g
 ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	_, err := d.sql(ctx, "astilba", "VACUUM ANALYZE server_metrics")
	return err
}

// cleanup removes only containers and networks carrying this run's ownership label.
func cleanup(d docker, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	filter := "label=" + ownerLabel + "=" + id
	if raw, err := d.run(ctx, "", "ps", "-aq", "--filter", filter); err == nil && raw != "" {
		_, _ = d.run(ctx, "", append([]string{"rm", "-f", "-v"}, strings.Fields(raw)...)...)
	}
	if raw, err := d.run(ctx, "", "network", "ls", "-q", "--filter", filter); err == nil && raw != "" {
		_, _ = d.run(ctx, "", append([]string{"network", "rm"}, strings.Fields(raw)...)...)
	}
}
