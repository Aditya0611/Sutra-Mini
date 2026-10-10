package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"

	"github.com/Aditya0611/Sutra-Mini/internal/envfile"
	syncworker "github.com/Aditya0611/Sutra-Mini/internal/sync"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := envfile.LoadUpwards(); err != nil {
		log.Fatal(err)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	worker := syncworker.New(pool, syncworker.Config{MockURL: os.Getenv("MOCK_URL")})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log.Printf("worker polling, mock %s", mockURL(os.Getenv("MOCK_URL")))
	err = worker.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func mockURL(v string) string {
	if v == "" {
		return "http://127.0.0.1:8090"
	}
	return v
}
