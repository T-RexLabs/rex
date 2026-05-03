// Command rex-central is the Rex central-node server.
//
// V1-skeleton today: the in-process minimum needed to develop the
// sync engine — `/sync/state`, `/sync/events`. Postgres, Docker
// Compose, multi-tenancy, and RBAC land later (see
// specs/central-node.yaml). The binary is the thin shell over
// internal/central/server (overview.SYS.1).
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/central/server"
)

// version is set at build time via -ldflags. Defaults to "dev" for
// local builds.
var version = "dev"

func main() {
	os.Exit(run(version, os.Args[1:]))
}

func run(version string, args []string) int {
	root := newRootCmd(version)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		// cobra prints the error already; just signal failure.
		return 1
	}
	return 0
}

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "rex-central",
		Short:         "Rex central-node server",
		Long:          "rex-central runs the central node that local nodes sync against.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newServeCmd())
	return root
}

func newServeCmd() *cobra.Command {
	var (
		addr            string
		shutdownTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the central HTTP server",
		Long: `Starts the in-process central server on --addr (default
127.0.0.1:8080). Honours SIGINT and SIGTERM with a graceful shutdown
of up to --shutdown-timeout (default 15s) before forcing exit.

V1 scope is the in-process minimum: in-memory event store, no auth,
no orgs. Production deployment with Postgres/Docker is post-v0.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := server.New(server.Options{})
			if err != nil {
				return fmt.Errorf("build server: %w", err)
			}
			httpSrv := &http.Server{
				Addr:              addr,
				Handler:           s.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"rex-central serving on %s (actor %s)\n",
				addr, s.Actor())

			errCh := make(chan error, 1)
			go func() {
				if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					errCh <- err
				}
				close(errCh)
			}()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			select {
			case err, ok := <-errCh:
				if ok && err != nil {
					return fmt.Errorf("listen: %w", err)
				}
			case <-ctx.Done():
				fmt.Fprintln(cmd.OutOrStdout(), "shutting down")
				shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer cancel()
				if err := httpSrv.Shutdown(shutdownCtx); err != nil {
					return fmt.Errorf("shutdown: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "TCP address to listen on")
	cmd.Flags().DurationVar(&shutdownTimeout, "shutdown-timeout", 15*time.Second, "max wait for graceful shutdown")
	return cmd
}
