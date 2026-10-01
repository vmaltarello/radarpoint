// Command radarpointd keeps the latest Radar-DPC products in memory and
// answers point queries over HTTP.
//
//	radarpointd --listen :8080 --products SRI,POH,TEMP
//	curl 'localhost:8080/now?lat=45.5966&lon=8.915'
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vmaltarello/radarpoint/internal/api"
	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/ingest"
	"github.com/vmaltarello/radarpoint/internal/store"
)

const userAgent = "radarpointd/0.1 (+https://github.com/vmaltarello/radarpoint)"

func main() {
	listen := flag.String("listen", ":8080", "HTTP listen address")
	products := flag.String("products", "SRI,POH,TEMP", "comma-separated product types to follow")
	window := flag.Duration("window", 30*time.Minute, "history kept in memory for each product")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var types []string
	for _, p := range strings.Split(*products, ",") {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !slices.Contains(dpc.ValidTypes, p) || p == "SITES" {
			log.Error("invalid product", "product", p)
			os.Exit(2)
		}
		types = append(types, p)
	}
	if len(types) == 0 {
		log.Error("no product to follow")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st := store.New()
	client := dpc.New(userAgent)
	var wg sync.WaitGroup
	for _, p := range types {
		poller := &ingest.Poller{Client: client, Store: st, Product: p, Window: *window, Log: log}
		wg.Go(func() { poller.Run(ctx) })
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           (&api.Server{Store: st, Products: types}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Info("listening", "addr", *listen, "products", types, "window", *window)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("HTTP server", "err", err)
		stop()
		wg.Wait()
		os.Exit(1)
	}
	wg.Wait()
	log.Info("stopped")
}
