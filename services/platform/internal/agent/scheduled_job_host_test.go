package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func nativeFilesAndJobs(t *testing.T, e *Engine, server uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	app, volume := uuid.New(), uuid.New()
	image := execution.TestImage
	p := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: 1, ExpiresAt: time.Now().Add(time.Hour), Images: map[string]string{app.String(): image}, Specification: domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "Scheduled worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Command: []string{"sleep", "86400"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/data"}}}}, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Files"}}}}.WithHash()
	if err := e.ensureStorage(ctx, p); err != nil {
		t.Fatal(err)
	}
	worker, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: deployment.UnitName(app), Config: &container.Config{Image: image, Cmd: []string{"sleep", "86400"}, Labels: visualLabels(p, app)}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(deployment.NetworkName(p.UnitID)), Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(volume), Target: "/data"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Docker.ContainerStart(ctx, worker.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.dataOutput(ctx, worker.ID, "sh", "-c", "printf 'file recovery fixture' > /data/record.txt; chmod 0640 /data/record.txt"); err != nil {
		t.Fatal(err)
	}
	_, expected, err := e.observeUnit(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Journal.saveUnit(unitState{Plan: p, Expected: expected}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	e.BackupRoots = append(e.BackupRoots, root)
	policy := backup.Policy{ID: uuid.New(), Name: "Files", WorkspaceID: uuid.New(), EnvironmentID: p.EnvironmentID, ServerID: server, DestinationID: uuid.New(), ResourceID: volume, Generation: 1, Method: "quiesced-files", Schedule: backup.Schedule{Timezone: "UTC", Hour: 12}, Retain: 2, Plan: p, Enabled: true}
	in := backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "Filesystem fixture", Kind: "filesystem", Root: root}, Access: backup.Access{Password: "file-repository-password-canary"}}
	if err = e.prepareRepository(ctx, &in, true); err != nil {
		t.Fatal(err)
	}
	s := scheduleState{Installation: in, Runs: []backup.Run{}}
	if err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil); err != nil {
		t.Fatalf("quiesced files: %v", err)
	}
	if len(s.Writers) > 0 {
		t.Fatal("file writers were not resumed")
	}
	state, err := e.Docker.ContainerInspect(ctx, worker.ID, client.ContainerInspectOptions{})
	if err != nil || !state.Container.State.Running {
		t.Fatal("file backup left writer stopped")
	}
	target := uuid.New()
	if err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), s.Runs[0].Snapshot, target); err != nil {
		t.Fatalf("file restore: %v", err)
	}
	helper, cleanup, err := e.dataContainer(ctx, policy, target, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := e.dataOutput(ctx, helper, "cat", "/data/record.txt")
	cleanup()
	if err != nil || string(data) != "file recovery fixture" {
		t.Fatal("restored file differs")
	}
	originalCLI := e.DockerCLI
	failing := filepath.Join(root, "failing-docker")
	if err = os.WriteFile(failing, []byte("#!/bin/sh\nexit 13\n"), 0700); err != nil {
		t.Fatal(err)
	}
	e.DockerCLI = failing
	err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil)
	e.DockerCLI = originalCLI
	if err == nil {
		t.Fatal("failed file export was accepted")
	}
	inspected, inspectErr := e.Docker.ContainerInspect(ctx, worker.ID, client.ContainerInspectOptions{})
	if inspectErr != nil || !inspected.Container.State.Running {
		t.Fatal("failed backup did not resume writer")
	}
	// The same scheduler executes one job while its management URL is unavailable.
	job := policy
	job.ID = uuid.New()
	job.Method = "container-job"
	job.ResourceID = app
	job.DestinationID = uuid.Nil
	job.Command = []string{"sh", "-c", "echo completed >> /data/jobs.txt"}
	now := time.Now().UTC()
	job.Schedule = backup.Schedule{Timezone: "UTC", Hour: now.Hour(), Minute: now.Minute()}
	if err = e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: job, Next: now.Truncate(time.Minute)}, Runs: []backup.Run{}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := e.Journal.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enrolled = true
	cfg.Origin = "https://127.0.0.1:1"
	if err = e.Journal.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	offline, cancel := context.WithCancel(ctx)
	client := NewClient(e.Journal, e)
	client.Observe = func(context.Context) api.AgentHeartbeat { return api.AgentHeartbeat{} }
	done := make(chan error, 1)
	go func() { done <- client.Run(offline) }()
	for i := 0; i < 100; i++ {
		state, err := e.Journal.schedule(job.ID)
		if err == nil && len(state.Runs) == 1 && state.Runs[0].State == "succeeded" {
			break
		}
		if i == 99 {
			cancel()
			<-done
			t.Fatalf("offline job not completed: %+v", state.Runs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	jobState, err := e.Journal.schedule(job.ID)
	if err != nil || jobState.Runs[0].ExitCode == nil || *jobState.Runs[0].ExitCode != 0 {
		t.Fatal("job exit result not persisted")
	}
	data, err = e.dataOutput(ctx, worker.ID, "cat", "/data/jobs.txt")
	if err != nil || strings.Count(string(data), "completed") != 1 {
		t.Fatal("job ran more than once")
	}
	raw, _ := json.Marshal(jobState.Runs)
	if strings.Contains(string(raw), "canary") {
		t.Fatal("job result leaked secrets")
	}
}
