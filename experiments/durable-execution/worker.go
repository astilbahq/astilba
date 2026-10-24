// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const schema = `CREATE TABLE operations (
 id text PRIMARY KEY, environment text NOT NULL, generation bigint NOT NULL,
 plan_hash text NOT NULL, run_id text NOT NULL, fence bigint NOT NULL DEFAULT 0,
 state text NOT NULL DEFAULT 'queued', cancel_requested boolean NOT NULL DEFAULT false,
 container_id text NOT NULL DEFAULT '', note text NOT NULL DEFAULT '',
 checkpoint text NOT NULL DEFAULT 'pending',
 observations integer NOT NULL DEFAULT 0, finalizations integer NOT NULL DEFAULT 0
)`

type jobArgs struct {
	ID string `json:"id"`
}

func (jobArgs) Kind() string { return "plat2_operation" }

type worker struct {
	river.WorkerDefaults[jobArgs]
	pool   *pgxpool.Pool
	socket string
	point  func(string)
}

func send(ctx context.Context, socket string, c command) (result, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	body, err := json.Marshal(c)
	if err != nil {
		return result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent/execute", bytes.NewReader(body))
	if err != nil {
		return result{}, err
	}
	response, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		return result{}, fmt.Errorf("agent %d: %s", response.StatusCode, body)
	}
	var r result
	err = json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&r)
	if err == nil && !c.samePlan(r.Command) {
		err = errors.New("agent returned another operation")
	}
	return r, err
}

func (w *worker) Work(ctx context.Context, job *river.Job[jobArgs]) error {
	var c command
	var checkpoint string
	err := w.pool.QueryRow(ctx, `UPDATE operations SET fence = fence + 1 WHERE id = $1
 RETURNING id, environment, generation, plan_hash, run_id, fence, cancel_requested, checkpoint`, job.Args.ID).
		Scan(&c.ID, &c.Environment, &c.Generation, &c.PlanHash, &c.Run, &c.Fence, &c.Cancel, &checkpoint)
	if err != nil {
		return err
	}
	w.point("claimed")
	if checkpoint == "pending" {
		r, err := send(ctx, w.socket, c)
		if err != nil {
			return err
		}
		w.point("response")
		tx, err := w.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.Background())
		changed, err := tx.Exec(ctx, `UPDATE operations SET state=$2, container_id=$3, note=$4, checkpoint='observed',
 observations=observations+1 WHERE id=$1 AND fence=$5`, c.ID, r.State, r.ContainerID, r.Note, c.Fence)
		if err != nil {
			return err
		}
		if changed.RowsAffected() != 1 {
			return errors.New("lost operation fence")
		}
		w.point("checkpoint_uncommitted")
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		w.point("checkpoint_committed")
	}
	// Intermediate checkpoints belong to application state. River's resumable
	// helper also mutates in-memory progress before its transaction commits.
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var currentFence uint64
	if err := tx.QueryRow(ctx, `SELECT fence, checkpoint FROM operations WHERE id=$1 FOR UPDATE`, c.ID).Scan(&currentFence, &checkpoint); err != nil {
		return err
	}
	if currentFence != c.Fence {
		return errors.New("lost operation fence")
	}
	if checkpoint != "finalized" {
		if _, err := tx.Exec(ctx, `UPDATE operations SET finalizations=finalizations+1, checkpoint='finalized' WHERE id=$1`, c.ID); err != nil {
			return err
		}
	}
	if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
		return err
	}
	w.point("completion_uncommitted")
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	w.point("completion_committed")
	return nil
}

func newQueue(pool *pgxpool.Pool, socket string, point func(string)) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &worker{pool: pool, socket: socket, point: point})
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Workers: workers, Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 2}},
		FetchCooldown: 10 * time.Millisecond, FetchPollInterval: 100 * time.Millisecond,
		JobTimeout: 4 * time.Second, RescueStuckJobsAfter: 5 * time.Second,
		RetryPolicy: quickRetry{},
	})
}

type quickRetry struct{}

func (quickRetry) NextRetry(*rivertype.JobRow) time.Time {
	return time.Now().Add(100 * time.Millisecond)
}

func enqueue(ctx context.Context, pool *pgxpool.Pool, queue *river.Client[pgx.Tx], c command, commit bool) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `INSERT INTO operations(id,environment,generation,plan_hash,run_id,cancel_requested)
 VALUES($1,$2,$3,$4,$5,$6)`, c.ID, c.Environment, c.Generation, c.PlanHash, c.Run, c.Cancel)
	if err != nil {
		return 0, err
	}
	inserted, err := queue.InsertTx(ctx, tx, jobArgs{ID: c.ID}, &river.InsertOpts{MaxAttempts: 10})
	if err != nil {
		return 0, err
	}
	if commit {
		err = tx.Commit(ctx)
	}
	return inserted.Job.ID, err
}
