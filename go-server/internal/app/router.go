// Router driver (GO-PLAN §12 R1 / REWRITE-V2 V2-M1): ROLE=router serves the
// hub server-list + login routing without running the game sim.
//
// Endpoints (single listener, see RouterAddr):
//   - / (any non-GET path too): hub shard sockets (hub.Server: register +
//     heartbeat + relay + roster; 3-miss eviction).
//   - GET /servers: login/hub server-list directing NEW sessions to the
//     newest healthy RUNNING shard: {"preferred":<addr>,
//     "shards":[{name,addr,buildId,gVer,state,load,newest}]}. preferred is
//     "" when no RUNNING shard is registered (clients keep their current
//     session / retry).
//   - GET /healthz: instance state/load (+ stamps); 503 once DRAINING.
//
// Env contract:
//   - ROLE=router (or --role router) selects this driver.
//   - HUB_LISTEN is the listen addr; else HUB_ADDR when it is a bare
//     host:port; else DefaultRouterAddr. (Shards dial HUB_ADDR as a
//     ws(s):// URL, unchanged.)
//   - HUB_TOKEN authenticates shard registrations (unchanged).
//   - DRAIN_TIMEOUT bounds router shutdown after SIGTERM (the router holds
//     no sim state, so it observes DRAINING on /healthz and exits).
package app

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"rpg-world-server/internal/hub"
	"rpg-world-server/internal/version"
)

// DefaultRouterAddr is the router listen addr when neither HUB_LISTEN nor a
// bare host:port HUB_ADDR is set (new port: never the game default 9001).
const DefaultRouterAddr = "127.0.0.1:9101"

// EnvHubListen overrides the router listen addr.
const EnvHubListen = "HUB_LISTEN"

// RouterAddr resolves the router listen addr: HUB_LISTEN, else HUB_ADDR
// when it is a bare host:port (hub-side form; ws(s):// URLs belong to
// shards), else DefaultRouterAddr.
func RouterAddr() string { return RouterAddrFromEnv(os.Getenv) }

// RouterAddrFromEnv is RouterAddr over an injected env lookup (tests).
func RouterAddrFromEnv(getenv func(string) string) string {
	if getenv != nil {
		if v := strings.TrimSpace(getenv(EnvHubListen)); v != "" {
			return v
		}
		if v := strings.TrimSpace(getenv(hub.EnvHubAddr)); v != "" {
			if !strings.Contains(v, "://") {
				return v
			}
		}
	}
	return DefaultRouterAddr
}

// ServerEntry is one /servers row.
type ServerEntry struct {
	Name    string `json:"name"`
	Addr    string `json:"addr"`
	BuildID string `json:"buildId,omitempty"`
	GVer    string `json:"gVer,omitempty"`
	State   string `json:"state"`
	Load    int    `json:"load"`
	Newest  bool   `json:"newest,omitempty"`
}

// ServerList is the GET /servers shape.
type ServerList struct {
	Preferred string        `json:"preferred"`
	Shards    []ServerEntry `json:"shards"`
}

// BuildServerList renders the hub table for login routing: shards newest
// first, Preferred pointing at the newest healthy RUNNING shard ("" when
// none — clients must not start new sessions anywhere).
func BuildServerList(h *hub.Server) ServerList {
	out := ServerList{}
	if h == nil {
		return out
	}
	infos := h.ListShards()
	out.Shards = make([]ServerEntry, 0, len(infos))
	for _, in := range infos {
		out.Shards = append(out.Shards, ServerEntry{
			Name: in.Name, Addr: in.Addr, BuildID: in.BuildID,
			GVer: in.GVer, State: in.State, Load: in.Load, Newest: in.Newest,
		})
	}
	if pref, ok := h.NewestRunning(); ok {
		out.Preferred = pref.Addr
		if out.Preferred == "" {
			out.Preferred = pref.Name
		}
	}
	return out
}

// RouterHandler wires the hub socket + /servers + /healthz on one mux. The
// caller owns listening; h must be non-nil (RunRouter always supplies one).
func RouterHandler(h *hub.Server, lc *Lifecycle) http.Handler {
	if lc == nil {
		lc = Default
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(BuildServerList(h))
	})
	mux.HandleFunc("GET /healthz", lc.ServeHealth)
	mux.Handle("/", h)
	return mux
}

// RouterDrainGrace is the router's SIGTERM grace: it holds no sim state,
// so DRAINING only needs to stay observable on /healthz (503) long enough
// for a balancer scrape before the process exits. Game/shard instances use
// DRAIN_TIMEOUT instead (empty-or-timeout with the sim running).
const RouterDrainGrace = 5 * time.Second

// awaitRouterDrain observes SIGTERM/SIGINT for the router: DRAINING (health
// flips to 503, shards keep heartbeating) for RouterDrainGrace, then exit.
// A second signal exits immediately.
func awaitRouterDrain(lc *Lifecycle) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	lc.BeginDraining()
	log.Printf("router: shutdown signal -> DRAINING (no new routing)")
	select {
	case <-ch:
	case <-time.After(RouterDrainGrace):
	}
	lc.MarkShutdown()
	log.Printf("router: SHUTDOWN")
	os.Exit(0)
}

// RunRouter serves the hub server-list until the listener fails or SIGTERM
// completes the drain. It runs no sim: SIGTERM observes DRAINING on
// /healthz, then the process exits (see awaitRouterDrain above).
func RunRouter(cfg Config) error {
	addr := RouterAddr()
	h := hub.NewServer(hub.SharedToken(), nil)
	h.StartSweeper(context.Background())
	lc := Default
	lc.SetLoad(0)
	go awaitRouterDrain(lc)
	log.Printf("router: hub listening on %s (buildID=%s gVer=%s)", addr, version.BuildID, version.GVer)
	return http.ListenAndServe(addr, RouterHandler(h, lc))
}
