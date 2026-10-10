package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/Aditya0611/Sutra-Mini/internal/envfile"
	"github.com/Aditya0611/Sutra-Mini/internal/issue"
	"github.com/Aditya0611/Sutra-Mini/internal/receipt"
	"github.com/Aditya0611/Sutra-Mini/internal/voucher"
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

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /jobwork/receipts", receipt.NewHandler(receipt.NewStore(pool)).Create)
	mux.HandleFunc("GET /issues/{id}", issue.NewHandler(pool).Get)
	mux.HandleFunc("GET /vouchers", voucher.NewHandler(pool).List)
	mux.HandleFunc("POST /vouchers/{id}/retry", voucher.NewHandler(pool).Retry)

	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Print("api listening on :8080")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
