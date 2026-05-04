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

	"github.com/asabla/rex/internal/central/config"
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
		configPath      string
		addr            string
		shutdownTimeout time.Duration
		keysFile        string
		dbDSN           string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the central HTTP server",
		Long: `Starts the central server. Configuration follows the precedence
defaults < /etc/rex/central.toml (or --config) < REX_CENTRAL_*
env vars < CLI flags. The bundled docker-compose deployment
mounts the config file at /etc/rex/central.toml and sets
REX_CENTRAL_DB_DSN via env so secrets stay out of the image.

Honours SIGINT and SIGTERM with a graceful shutdown of up to
--shutdown-timeout (default 15s) before forcing exit.

When --keys <file> is set, the server loads an authorized-keys
TOML file and verifies every pushed event's signature
(sync.SEC.1). Without --keys, signature verification is skipped
— dev/test path only.

When --db <dsn> is set, events persist to Postgres via the
schema the central migrates on startup (central-node.DB.*).
Without --db, the server uses an in-memory store; events are
lost on restart.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := config.PathOrDefault(configPath)
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			cfg.Resolve(cfgPath)

			// CLI flags overlay last; only treat a flag as set
			// when its current value is non-empty (string flags)
			// or when the user explicitly set it via cobra
			// (durations, where 0 isn't a sentinel).
			if cmd.Flags().Changed("addr") {
				cfg.Server.Addr = addr
			}
			if cmd.Flags().Changed("shutdown-timeout") {
				cfg.Server.ShutdownTimeout = shutdownTimeout
			}
			if cmd.Flags().Changed("keys") {
				cfg.Auth.KeysFile = keysFile
			}
			if cmd.Flags().Changed("db") {
				cfg.DB.DSN = dbDSN
			}

			opts := server.Options{}
			if cfg.Auth.KeysFile != "" {
				ks, err := server.LoadKeystoreFile(cfg.Auth.KeysFile)
				if err != nil {
					return err
				}
				opts.Keystore = ks
				fmt.Fprintf(cmd.OutOrStdout(),
					"loaded %d authorized key(s) from %s\n",
					len(ks.Handles()), cfg.Auth.KeysFile)
			}
			if cfg.DB.DSN != "" {
				pg, err := server.NewPostgresStore(cmd.Context(), cfg.DB.DSN)
				if err != nil {
					return fmt.Errorf("postgres store: %w", err)
				}
				defer pg.Close()
				opts.Store = pg
				fmt.Fprintln(cmd.OutOrStdout(),
					"using postgres event store (schema migrated)")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(),
					"WARNING: no db dsn configured — using in-memory store; events lost on restart")
			}
			s, err := server.New(opts)
			if err != nil {
				return fmt.Errorf("build server: %w", err)
			}
			httpSrv := &http.Server{
				Addr:              cfg.Server.Addr,
				Handler:           s.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"rex-central serving on %s (actor %s)\n",
				cfg.Server.Addr, s.Actor())

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
				shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
				defer cancel()
				if err := httpSrv.Shutdown(shutdownCtx); err != nil {
					return fmt.Errorf("shutdown: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to central.toml (default: /etc/rex/central.toml; missing file is OK)")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "TCP address to listen on (overrides config + REX_CENTRAL_ADDR)")
	cmd.Flags().DurationVar(&shutdownTimeout, "shutdown-timeout", 15*time.Second, "max wait for graceful shutdown (overrides config + REX_CENTRAL_SHUTDOWN_TIMEOUT)")
	cmd.Flags().StringVar(&keysFile, "keys", "", "path to authorized-keys TOML file (overrides config + REX_CENTRAL_KEYS_FILE)")
	cmd.Flags().StringVar(&dbDSN, "db", "", "Postgres DSN (overrides config + REX_CENTRAL_DB_DSN); empty uses in-memory store")
	return cmd
}
