package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type note struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Body string `json:"body"`
}

func main() {
	token := os.Getenv("WRITE_TOKEN")
	if len(token) < 32 || os.Getenv("DATABASE_URL") == "" {
		log.Fatal("DATABASE_URL and a WRITE_TOKEN of at least 32 bytes are required")
	}
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("database configuration failed")
	}
	defer pool.Close()
	expected := sha256.Sum256([]byte("Bearer " + token))
	revision := os.Getenv("APP_REVISION")
	if revision == "" {
		revision = "baseline"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if os.Getenv("FAIL_READINESS") == "true" || pool.Ping(ctx) != nil {
			http.Error(w, "not ready", 503)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"revision": revision})
	})
	mux.HandleFunc("GET /notes", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		rows, err := pool.Query(ctx, "SELECT id,slug,body FROM comparison_notes ORDER BY id LIMIT 1000")
		if err != nil {
			http.Error(w, "query failed", 503)
			return
		}
		defer rows.Close()
		out := []note{}
		for rows.Next() {
			var n note
			if rows.Scan(&n.ID, &n.Slug, &n.Body) != nil {
				http.Error(w, "query failed", 503)
				return
			}
			out = append(out, n)
		}
		if rows.Err() != nil {
			http.Error(w, "query failed", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /notes", func(w http.ResponseWriter, r *http.Request) {
		supplied := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var n struct {
			Slug string `json:"slug"`
			Body string `json:"body"`
		}
		if decoder.Decode(&n) != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) || n.Slug == "" || len(n.Slug) > 80 || len(n.Body) > 1024 {
			http.Error(w, "invalid note", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var id int64
		if err := pool.QueryRow(ctx, "INSERT INTO comparison_notes(slug,body) VALUES($1,$2) RETURNING id", n.Slug, n.Body).Scan(&id); err != nil {
			status := http.StatusServiceUnavailable
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				status = http.StatusConflict
			}
			http.Error(w, "write failed", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(note{ID: id, Slug: n.Slug, Body: n.Body})
	})
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	log.Fatal(server.ListenAndServe())
}
