package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/Aditya0611/Sutra-Mini/internal/envfile"
	"github.com/jackc/pgx/v5"
)

func main() {
	if err := envfile.LoadUpwards(); err != nil {
		log.Fatal(err)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is required")
	}
	sqlBytes, err := readMigration()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.skus') IS NOT NULL`).Scan(&exists); err != nil {
		log.Fatal(err)
	}
	if exists {
		log.Print("schema already present, skipping")
		return
	}

	mrr := conn.PgConn().Exec(ctx, string(sqlBytes))
	for mrr.NextResult() {
		if _, err := mrr.ResultReader().Close(); err != nil {
			_ = mrr.Close()
			log.Fatal(err)
		}
	}
	if err := mrr.Close(); err != nil {
		log.Fatal(err)
	}
	log.Print("applied migrations/001_init.sql")
}

func readMigration() ([]byte, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for {
		path := filepath.Join(dir, "migrations", "001_init.sql")
		b, err := os.ReadFile(path)
		if err == nil {
			return b, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, os.ErrNotExist
		}
		dir = parent
	}
}
