package httpapi

import (
	"context"
	"net/http"
	"time"
)

func (a *App) live(w http.ResponseWriter, _ *http.Request) {
	// Never touches the database, so a database outage does not get every
	// instance restarted.
	writeJSON(w, http.StatusOK, struct {
		Status        string `json:"status"`
		UptimeSeconds int64  `json:"uptimeSeconds"`
	}{"ok", int64(time.Since(a.started).Round(time.Second) / time.Second)})
}

type readyChecks struct {
	Postgres string `json:"postgres"`
	Shutdown string `json:"shutdown"`
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	checks := readyChecks{Postgres: "fail", Shutdown: "ok"}
	if a.shuttingDown.Load() {
		checks.Shutdown = "fail"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err == nil {
		checks.Postgres = "ok"
	}
	status, code := "ready", http.StatusOK
	if checks.Postgres != "ok" || checks.Shutdown != "ok" {
		status, code = "not ready", http.StatusServiceUnavailable
	}
	writeJSON(w, code, struct {
		Status string      `json:"status"`
		Checks readyChecks `json:"checks"`
	}{status, checks})
}
