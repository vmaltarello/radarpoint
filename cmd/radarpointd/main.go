// Command radarpointd keeps the latest Radar-DPC products in memory and
// answers point queries over HTTP.
//
//	radarpointd --listen :8080 --products SRI,POH,TEMP
//	curl 'localhost:8080/v1/now?lat=45.5966&lon=8.915'
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
	"github.com/vmaltarello/radarpoint/internal/irene"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/store"
)

const userAgent = "radarpointd/0.1 (+https://github.com/vmaltarello/radarpoint)"

func main() {
	listen := flag.String("listen", ":8080", "HTTP listen address")
	products := flag.String("products", "SRI,POH,TEMP", "comma-separated product types to follow")
	window := flag.Duration("window", 30*time.Minute, "history kept in memory for each product")
	ireneURL := flag.String("irene-url", "", "URL of the IRENE service, e.g. http://irene:8000 (empty: extrapolation only)")
	ireneMembers := flag.Int("irene-members", 4, "IRENE ensemble members (1–10): more is better and slower")
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

	// The rain nowcast is recomputed from the SRI frames each time a new one
	// arrives: 12 steps of 5 minutes. The latest POH frame, if followed, is
	// moved along with the rain.
	sri, _ := dpc.Lookup("SRI")
	poh, _ := dpc.Lookup("POH")
	engine := &nowcast.Engine{Steps: 12, Options: nowcast.DefaultOptions,
		IsNoData: sri.IsNoData, HailIsNoData: poh.IsNoData}
	// IRENE, if configured, runs in the background after each new nowcast;
	// until its forecast is ready the extrapolation is served.
	var runner *irene.Runner
	if *ireneURL != "" {
		client := irene.NewClient(*ireneURL)
		client.Members = *ireneMembers
		runner = &irene.Runner{Client: client, Steps: engine.Steps, Done: func(fc *irene.Forecast, err error) {
			if err != nil {
				log.Warn("IRENE forecast failed", "err", err)
				return
			}
			log.Info("IRENE forecast ready", "base", fc.Base, "members", fc.Members, "took", fc.Took.Round(time.Second))
		}}
	}

	updateNowcast := func() {
		start := time.Now()
		n, err := engine.Update(st.Frames("SRI"))
		if err != nil {
			log.Info("nowcast not updated", "reason", err)
			return
		}
		log.Info("nowcast updated", "base", n.Base, "pairs", n.Pairs,
			"tracked_blocks", n.Motion.Measured, "took", time.Since(start).Round(time.Millisecond))
		if runner != nil {
			runner.Request(ctx, n)
		}
	}

	var wg sync.WaitGroup
	for _, p := range types {
		poller := &ingest.Poller{Client: client, Store: st, Product: p, Window: *window, Log: log}
		switch p {
		case "SRI":
			poller.OnUpdate = updateNowcast
		case "POH":
			poller.OnUpdate = func() {
				if err := engine.SetHail(st.Latest("POH")); err != nil {
					log.Warn("hail frame not usable", "err", err)
				}
			}
		}
		wg.Go(func() { poller.Run(ctx) })
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           apiServer(st, types, engine, runner).Handler(),
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

// apiServer serves /nowcast only when SRI, the product it is based on, is
// followed.
func apiServer(st *store.Store, products []string, engine *nowcast.Engine, runner *irene.Runner) *api.Server {
	s := &api.Server{Store: st, Products: products}
	if slices.Contains(products, "SRI") {
		s.Nowcast = engine.Current
		if runner != nil {
			s.Irene = runner.Current
		}
	}
	return s
}
