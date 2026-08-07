// Package checker performs a single health check against a target.
package checker

import (
	"database/sql"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql" // mysql:// scheme
	_ "github.com/lib/pq"              // postgres:// scheme

	"github.com/sagargoswami2001/sentinel/internal/config"
)

// Result is the outcome of checking one target.
type Result struct {
	Target       config.Target
	Up           bool
	StatusCode   int           // 0 when the request itself failed
	Latency      time.Duration
	CertDaysLeft int           // -1 when the target is not HTTPS
	Err          error         // network-level failure, nil otherwise
}

// Run checks a single target and returns the result.
//
// Scheme routing:
//   - mysql://user:pass@host:3306/db  → real MySQL connection + Ping
//   - postgres://user:pass@host/db    → real Postgres connection + Ping
//   - tcp://host:port                 → raw TCP dial (SSH, Redis, …)
//   - http:// / https://              → full HTTP check with TLS expiry
func Run(t config.Target) Result {
	res := Result{Target: t, CertDaysLeft: -1}

	switch {
	case strings.HasPrefix(t.URL, "mysql://"):
		return checkDB("mysql", strings.TrimPrefix(t.URL, "mysql://"), t, res)

	case strings.HasPrefix(t.URL, "postgres://"), strings.HasPrefix(t.URL, "postgresql://"):
		return checkDB("postgres", t.URL, t, res)

	case strings.HasPrefix(t.URL, "tcp://"):
		addr := strings.TrimPrefix(t.URL, "tcp://")
		start := time.Now()
		conn, err := net.DialTimeout("tcp", addr, t.Timeout())
		res.Latency = time.Since(start)
		if err != nil {
			res.Err = err
			return res
		}
		conn.Close()
		res.Up = true
		return res
	}

	// Default: HTTP/HTTPS check.
	client := &http.Client{Timeout: t.Timeout()}
	start := time.Now()
	resp, err := client.Get(t.URL)
	res.Latency = time.Since(start)
	if err != nil {
		res.Err = err
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode
	res.Up = resp.StatusCode == t.ExpectStatus
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		expiry := resp.TLS.PeerCertificates[0].NotAfter
		res.CertDaysLeft = int(time.Until(expiry).Hours() / 24)
	}
	return res
}

// checkDB opens a db connection, calls Ping, and closes it.
// Opening (not pooling) on every check keeps state simple and ensures
// we test the full connection path each time.
func checkDB(driver, dsn string, t config.Target, res Result) Result {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		res.Err = err
		return res
	}
	defer db.Close()
	db.SetConnMaxLifetime(t.Timeout())

	start := time.Now()
	err = db.Ping()
	res.Latency = time.Since(start)
	if err != nil {
		res.Err = err
		return res
	}
	res.Up = true
	return res
}

// RunAll checks every target concurrently — one goroutine per target.
func RunAll(targets []config.Target) []Result {
	results := make([]Result, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t config.Target) {
			defer wg.Done()
			results[i] = Run(t)
		}(i, t)
	}
	wg.Wait()
	return results
}

