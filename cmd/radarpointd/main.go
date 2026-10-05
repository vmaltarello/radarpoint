// Command radarpointd keeps the latest Radar-DPC products in memory and
// answers point queries over HTTP.
//
//	radarpointd --listen :8080 --products SRI,POH,TEMP,VIL,ETM
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
	"github.com/vmaltarello/radarpoint/internal/grids"
	"github.com/vmaltarello/radarpoint/internal/hailrisk"
	"github.com/vmaltarello/radarpoint/internal/ingest"
	"github.com/vmaltarello/radarpoint/internal/irene"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
	"github.com/vmaltarello/radarpoint/internal/store"
)

const userAgent = "radarpointd/0.1 (+https://github.com/vmaltarello/radarpoint)"

func main() {
	listen := flag.String("listen", ":8080", "HTTP listen address")
	products := flag.String("products", "SRI,POH,TEMP,VIL,ETM", "comma-separated product types to follow; SRI, POH, VIL and ETM together enable the hail probability")
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
	// The whole-grid frames for map clients are rebuilt whenever one of
	// their inputs changes: the nowcast, its hail, IRENE's forecast or the
	// hail probability.
	var runner *irene.Runner
	hail := &hailrisk.Tracker{}
	builder := &grids.Builder{}
	observedHail := func(at time.Time) *nowcast.Field {
		for _, f := range st.Frames("POH") {
			if f.Time.Equal(at) {
				fld, err := nowcast.NewField(f.Grid, poh.IsNoData)
				if err != nil {
					log.Warn("hail frame not usable", "err", err)
					return nil
				}
				return fld
			}
		}
		return nil
	}
	var gridsMu sync.Mutex // inputs are read and encoded in order
	updateGrids := func() {
		gridsMu.Lock()
		defer gridsMu.Unlock()
		start := time.Now()
		in := grids.Inputs{Nowcast: engine.Current(), Risk: hail.Current(), ObservedHail: observedHail}
		if runner != nil {
			in.Irene = runner.Current()
		}
		if builder.Update(in) {
			set := builder.Current()
			log.Info("grids encoded", "frames", len(set.Frames), "method", set.Method, "took", time.Since(start).Round(time.Millisecond))
		}
	}

	// IRENE, if configured, runs in the background after each new nowcast;
	// until its forecast is ready the extrapolation is served.
	if *ireneURL != "" {
		client := irene.NewClient(*ireneURL)
		client.Members = *ireneMembers
		runner = &irene.Runner{Client: client, Steps: engine.Steps, Done: func(fc *irene.Forecast, err error) {
			if err != nil {
				log.Warn("IRENE forecast failed", "err", err)
				return
			}
			log.Info("IRENE forecast ready", "base", fc.Base, "members", fc.Members, "took", fc.Took.Round(time.Second))
			updateGrids()
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

	// The probability of hail within 30 minutes needs the nowcast and POH,
	// VIL and ETM for its base time; it is recomputed whenever one of them
	// arrives, once all are there.
	hailInputs := []string{"SRI", "POH", "VIL", "ETM"}
	followsHail := !slices.ContainsFunc(hailInputs, func(p string) bool { return !slices.Contains(types, p) })
	frameAt := func(product string, at time.Time) *raster.GeoTIFF {
		for _, f := range st.Frames(product) {
			if f.Time.Equal(at) {
				return f.Grid
			}
		}
		return nil
	}
	updateHail := func() {
		if !followsHail {
			return
		}
		start := time.Now()
		ok, err := hail.Update(engine.Current(), frameAt)
		if err != nil {
			log.Warn("hail probability not computed", "err", err)
			return
		}
		if ok {
			log.Info("hail probability updated", "base", hail.Current().Base, "took", time.Since(start).Round(time.Millisecond))
		}
	}

	var wg sync.WaitGroup
	for _, p := range types {
		poller := &ingest.Poller{Client: client, Store: st, Product: p, Window: *window, Log: log}
		switch p {
		case "SRI":
			poller.OnUpdate = func() {
				updateNowcast()
				updateHail()
				updateGrids()
			}
		case "POH":
			poller.OnUpdate = func() {
				if err := engine.SetHail(st.Latest("POH")); err != nil {
					log.Warn("hail frame not usable", "err", err)
				}
				updateHail()
				updateGrids()
			}
		case "VIL", "ETM":
			poller.OnUpdate = func() {
				updateHail()
				updateGrids()
			}
		}
		wg.Go(func() { poller.Run(ctx) })
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           apiServer(st, types, engine, runner, followsHail, hail, builder).Handler(),
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

// apiServer serves /nowcast and /grids only when SRI, the product they are
// based on, is followed.
func apiServer(st *store.Store, products []string, engine *nowcast.Engine, runner *irene.Runner, followsHail bool, hail *hailrisk.Tracker, builder *grids.Builder) *api.Server {
	s := &api.Server{Store: st, Products: products}
	if slices.Contains(products, "SRI") {
		s.Nowcast = engine.Current
		if runner != nil {
			s.Irene = runner.Current
		}
		if followsHail {
			s.HailRisk = hail.Current
		}
		s.Grids = builder.Current
	}
	return s
}
