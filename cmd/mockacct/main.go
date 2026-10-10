package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/Aditya0611/Sutra-Mini/internal/envfile"
	"github.com/Aditya0611/Sutra-Mini/internal/mockacct"
)

func main() {
	if err := envfile.LoadUpwards(); err != nil {
		log.Fatal(err)
	}
	mock := mockacct.New()
	if v := os.Getenv("MOCK_FAIL_RATE"); v != "" {
		rate, err := strconv.ParseFloat(v, 64)
		if err != nil || rate < 0 || rate > 1 {
			log.Fatal("MOCK_FAIL_RATE must be between 0 and 1")
		}
		mock.SetFailRate(rate)
	}
	if v := os.Getenv("MOCK_HANG_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 0 {
			log.Fatal("MOCK_HANG_MS must be a number of milliseconds")
		}
		mock.SetHang(time.Duration(ms) * time.Millisecond)
	}
	server := &http.Server{
		Addr:              ":8090",
		Handler:           mock.Handler(),
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
	log.Print("mock accounting listening on :8090")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
