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
		keysFile        string
		dbDSN           string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the central HTTP server",
		Long: `Starts the central server on --addr (default 127.0.0.1:8080).
Honours SIGINT and SIGTERM with a graceful shutdown of up to
--shutdown-timeout (default 15s) before forcing exit.

When --keys <file> is set, the server loads an authorized-keys TOML
file and verifies every pushed event's signature (sync.SEC.1).
Records signed by unregistered fingerprints or with invalid
signatures are rejected with 401. Without --keys, signature
verification is skipped (dev/test path only).

When --db <dsn> is set, events persist to Postgres via the schema
the central node migrates on startup (central-node.DB.*). Without
--db, the server uses an in-memory store — convenient for dev/test
but loses every event on restart.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := server.Options{}
			if keysFile != "" {
				ks, err := server.LoadKeystoreFile(keysFile)
				if err != nil {
					return err
				}
				opts.Keystore = ks
				fmt.Fprintf(cmd.OutOrStdout(),
					"loaded %d authorized key(s) from %s\n",
					len(ks.Handles()), keysFile)
			}
			if dbDSN != "" {
				pg, err := server.NewPostgresStore(cmd.Context(), dbDSN)
				if err != nil {
					return fmt.Errorf("postgres store: %w", err)
				}
				defer pg.Close()
				opts.Store = pg
				fmt.Fprintln(cmd.OutOrStdout(),
					"using postgres event store (schema migrated)")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(),
					"WARNING: no --db dsn — using in-memory store; events lost on restart")
			}
			s, err := server.New(opts)
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
	cmd.Flags().StringVar(&keysFile, "keys", "", "path to authorized-keys TOML file (signature verification off when empty)")
	cmd.Flags().StringVar(&dbDSN, "db", "", "Postgres DSN (postgres://...); empty uses in-memory store")
	return cmd
}
