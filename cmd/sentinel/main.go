// Sentinel is a self-hosted uptime monitoring and alerting service.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sagargoswami2001/sentinel/internal/checker"
	"github.com/sagargoswami2001/sentinel/internal/config"
	"github.com/sagargoswami2001/sentinel/internal/report"
	"github.com/sagargoswami2001/sentinel/internal/server"
)

var version = "0.1.0-dev"

func main() {
	var cfgPath string

	root := &cobra.Command{
		Use:     "sentinel",
		Short:   "Sentinel — uptime monitoring for your services",
		Version: version,
	}

	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Run every configured check once and print a report",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			results := checker.RunAll(cfg.Targets)
			down := report.Print(results)

			// Non-zero exit when anything is down, so this command
			// works in scripts and CI pipelines out of the box.
			if down > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	checkCmd.Flags().StringVarP(&cfgPath, "config", "c",
		"configs/sentinel.yaml", "path to the config file")

	var listen string
	var interval time.Duration
	var publicMode bool

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run checks continuously and serve the web dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			// SENTINEL_PUBLIC=1 env var activates per-visitor sessions,
			// so every browser gets its own fresh board. Useful on
			// Render / any public URL where strangers try the tool.
			if os.Getenv("SENTINEL_PUBLIC") == "1" {
				publicMode = true
			}

			srv, err := server.New(cfg, cfgPath, interval, publicMode)
			if err != nil {
				return err
			}

			go srv.Watch(context.Background())

			mode := "normal"
			if srv.Public() {
				mode = "public demo (per-visitor sessions)"
			}
			fmt.Printf("sentinel: mode=%s, %d targets, interval=%s\n",
				mode, len(cfg.Targets), interval)
			fmt.Printf("sentinel: dashboard on http://localhost%s\n", listen)
			return http.ListenAndServe(listen, srv.Handler())
		},
	}
	serveCmd.Flags().StringVarP(&cfgPath, "config", "c",
		"configs/sentinel.yaml", "path to the config file")
	serveCmd.Flags().StringVarP(&listen, "listen", "l",
		":8080", "address to serve the dashboard on")
	serveCmd.Flags().DurationVarP(&interval, "interval", "i",
		30*time.Second, "how often to re-run all checks")
	serveCmd.Flags().BoolVar(&publicMode, "public", false,
		"give each visitor their own private board (set SENTINEL_PUBLIC=1 on Render)")

	root.AddCommand(checkCmd, serveCmd)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
