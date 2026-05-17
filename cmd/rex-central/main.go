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

	"github.com/asabla/rex/internal/central/backup"
	"github.com/asabla/rex/internal/central/config"
	"github.com/asabla/rex/internal/central/server"
	centralweb "github.com/asabla/rex/internal/central/web"
	"github.com/asabla/rex/internal/cmdhelp"
	"github.com/asabla/rex/internal/core/identity"
	internalweb "github.com/asabla/rex/internal/web"
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
		Use:   "rex-central",
		Short: "Rex central-node server",
		Long:  "rex-central runs the central node that local nodes sync against.",
		Example: `  rex-central serve --config /etc/rex/central.toml
  rex-central backup --config /etc/rex/central.toml --output /var/backups/rex
  rex-central restore --config /etc/rex/central.toml --from /var/backups/rex.dump`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	cmdhelp.SetRelated(root,
		"rex-central serve --config /etc/rex/central.toml",
		"rex-central backup --config /etc/rex/central.toml --output /var/backups/rex",
		"rex-central restore --config /etc/rex/central.toml --from /var/backups/rex.dump",
	)
	cmdhelp.InstallRelatedHelp(root)
	root.AddCommand(newServeCmd())
	root.AddCommand(newBackupCmd())
	root.AddCommand(newRestoreCmd())
	return root
}

func newServeCmd() *cobra.Command {
	var (
		configPath      string
		addr            string
		shutdownTimeout time.Duration
		keysFile        string
		dbDSN           string
		logLevel        string
		logFormat       string
		webEnabled      bool
		devMode         bool
		devIdentityDir  string
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
		Example: `  rex-central serve
  rex-central serve --config /etc/rex/central.toml
  rex-central serve --config /etc/rex/central.toml --db 'postgres://user:pass@host/db'`,
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
			if cmd.Flags().Changed("log-level") {
				cfg.Log.Level = logLevel
			}
			if cmd.Flags().Changed("log-format") {
				cfg.Log.Format = logFormat
			}

			// Build the structured logger from the resolved
			// config and pass it into Server.New. HEALTH.3:
			// JSON to stdout in production. Tests inject their
			// own writer via cmd.SetOut() if they need to assert
			// log content.
			logger := server.NewLogger(server.LogConfig{
				Output: cmd.OutOrStdout(),
				Level:  server.ParseLevel(cfg.Log.Level),
				Format: cfg.Log.Format,
			})

			opts := server.Options{Logger: logger}
			var ks *server.Keystore
			if cfg.Auth.KeysFile != "" {
				loaded, err := server.LoadKeystoreFile(cfg.Auth.KeysFile)
				if err != nil {
					return err
				}
				ks = loaded
				opts.Keystore = ks
				logger.Info("authorized keys loaded",
					"op", "startup",
					"keys_file", cfg.Auth.KeysFile,
					"count", len(ks.Handles()),
				)
			}

			// --dev: register the local default identity (the one
			// `rex` uses) as an authorized key + flip --web on.
			// Admin-membership in the default org happens after
			// pg opens (below). Loud warning so this never lights
			// up by accident.
			var devFingerprint identity.Fingerprint
			if devMode {
				dir := devIdentityDir
				if dir == "" {
					def, derr := identity.DefaultStoreDir()
					if derr != nil {
						return fmt.Errorf("dev mode: resolve identity dir: %w", derr)
					}
					dir = def
				}
				store := identity.NewStore(dir)
				signer, err := identity.EnsureDefaultStoreSigner(store)
				if err != nil {
					return fmt.Errorf("dev mode: ensure identity: %w", err)
				}
				if ks == nil {
					ks = server.NewKeystore()
					opts.Keystore = ks
				}
				fp, err := ks.Add(string(signer.Handle()), signer.PublicKey())
				if err != nil {
					return fmt.Errorf("dev mode: register identity in keystore: %w", err)
				}
				devFingerprint = fp
				webEnabled = true
				logger.Warn("DEV MODE: registered local default identity as authorized key — never use --dev in production",
					"op", "startup.dev",
					"identity_dir", dir,
					"fingerprint", fp.String(),
				)
			}
			var pg *server.PostgresStore
			if cfg.DB.DSN != "" {
				var err error
				pg, err = server.NewPostgresStore(cmd.Context(), cfg.DB.DSN)
				if err != nil {
					return fmt.Errorf("postgres store: %w", err)
				}
				defer pg.Close()
				opts.Store = pg
				// Same pool backs the git-merged content store.
				// PostgresGitStore uses the parent's withOrgScope
				// helper so RLS scoping carries through.
				opts.GitStore = server.NewPostgresGitStore(pg)
				logger.Info("postgres store opened",
					"op", "startup",
					"store", "postgres",
				)
				// AUTH.2.2: overlay runtime-registered keys from
				// authorized_keys onto the in-memory Keystore so
				// invite-redeemed identities authenticate without
				// waiting for a TOML rewrite. The TOML file (loaded
				// above) covers bootstrap + dev fallback; the DB
				// overlay covers everything redeem-flow added.
				if ks == nil {
					ks = server.NewKeystore()
					opts.Keystore = ks
				}
				if n, err := server.LoadAuthorizedKeysIntoKeystore(cmd.Context(), ks, pg, logger); err != nil {
					return fmt.Errorf("overlay authorized_keys: %w", err)
				} else if n > 0 {
					logger.Info("authorized keys overlaid from db",
						"op", "startup",
						"count", n,
					)
				}
				if devMode && !devFingerprint.IsZero() {
					// Promote the dev identity to admin of the
					// default org so the web UI's per-page RBAC
					// checks pass on first request — no need to
					// run the bootstrap-token redeem dance.
					if err := pg.EnsureAdminMembership(cmd.Context(), devFingerprint.String()); err != nil {
						return fmt.Errorf("dev mode: ensure admin membership: %w", err)
					}
					logger.Warn("DEV MODE: dev identity promoted to admin of the default org",
						"op", "startup.dev",
						"fingerprint", devFingerprint.String(),
					)
				}
			} else {
				logger.Warn("no db dsn configured — using in-memory store; events lost on restart",
					"op", "startup",
					"store", "memory",
				)
			}
			// Build the audit appender before constructing the
			// server so opts.AuthAudit can carry it through.
			// Without this the auth.go appendAuthAudit calls
			// silently dropped every auth event in production
			// (the field was wired but always nil). The
			// appender targets the same PostgresStore the events
			// flow already uses; in-memory mode (no --db) gets
			// nil and auth events continue to drop silently —
			// that's the dev/test path.
			var auditAppender *server.PostgresAuditAppender
			if pg != nil {
				auditAppender = server.NewPostgresAuditAppender(pg, identity.Actor{
					Role: identity.RoleCentral,
					// Fingerprint stamps below once we have a
					// server.Server (its Actor() includes the
					// generated keypair's fingerprint).
				})
			}
			if auditAppender != nil {
				opts.AuthAudit = auditAppender
			}
			s, err := server.New(opts)
			if err != nil {
				return fmt.Errorf("build server: %w", err)
			}
			if auditAppender != nil {
				// Restamp the appender's actor now that the
				// server has minted its keypair. The first
				// appender construction above was needed to
				// satisfy opts.AuthAudit; this swap aligns
				// audit entries with the central node's true
				// actor string before the listener accepts
				// traffic.
				auditAppender = server.NewPostgresAuditAppender(pg, s.Actor())
				opts.AuthAudit = auditAppender
			}

			// Mount the web shell as a fallback handler when --web
			// is set (web-ui.CENTRAL-LAYOUT.1). Off by default
			// until central-read-side-pages lands real surfaces;
			// constructing centralweb.New here proves the shared
			// internal/web package is reachable from the central
			// binary.
			if webEnabled {
				webOpts := centralweb.Options{
					Version:  version,
					BindAddr: cfg.Server.Addr,
					Auth:     centralAuthAdapter{s}, // bridges *server.Server's ValidatedSession to centralweb.SessionInfo
					// The central GitStore satisfies the
					// centralweb.GitEntityReader subset (Get + List);
					// the event Store satisfies centralweb.EventReader
					// (Since). The workspace resolver maps a ws-id to
					// projections backed by both. When pg is wired
					// (--db Postgres), the resolver also carries a
					// PostgresSearch backend so /search runs real
					// queries; in-memory dev mode falls back to the
					// "search backend not yet wired" notice.
					Resolver: func() internalweb.WorkspaceResolver {
						if pg != nil {
							return centralweb.NewGitStoreResolverWithSearch(
								s.GitStore(),
								s.Store(),
								searchAdapter{search: server.NewPostgresSearch(pg)},
							)
						}
						return centralweb.NewGitStoreResolver(s.GitStore(), s.Store())
					}(),
				}
				if pg != nil {
					// Org-admin pages need a workspace-id-independent
					// read against the org / membership tables. The
					// memory store has no org concept, so wire the
					// adapter only when --db (Postgres) is on. The
					// adapter also takes the audit appender so
					// mutations emit org.member.* audit events on
					// success (CENTRAL.3).
					webOpts.Orgs = newPostgresOrgsAdapter(pg, auditAppender)
					// Invite redeem flow (AUTH.2.1): the
					// adapter wraps PostgresStore.RedeemInvite +
					// overlays the new key into the in-memory
					// keystore + emits identity.key_registered
					// and org.member.joined audit events.
					webOpts.Redeemer = newPostgresInviteRedeemer(pg, ks, auditAppender)
					// Org-scoped /orgs/<id>/audit: scopes the
					// central event store by app.current_org_id
					// via WithOrgID + the shared audit filter.
					webOpts.OrgAudit = newPostgresOrgAuditProjection(pg)
				}
				webShell, err := centralweb.New(webOpts)
				if err != nil {
					return fmt.Errorf("build web shell: %w", err)
				}
				s.MountWeb(webShell.Handler())
				logger.Info("central web shell mounted",
					"op", "startup",
					"flag", "--web",
				)
			}
			httpSrv := &http.Server{
				Addr:              cfg.Server.Addr,
				Handler:           s.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}

			// Announce / persist the admin bootstrap token if
			// the central is in bootstrap mode (no admin yet).
			// Logs at WARN with the token + writes to
			// /var/lib/rex/bootstrap.token by default. After
			// redemption the file is auto-cleaned on the next
			// startup. Configurable via central.toml's
			// [bootstrap] section if a different path fits the
			// deployment.
			s.AnnounceBootstrap(cmd.Context(), cfg.Bootstrap.TokenFile)

			logger.Info("rex-central listening",
				"op", "startup",
				"addr", cfg.Server.Addr,
				"actor", s.Actor().String(),
				"version", version,
			)

			// Backup scheduler: opt-in via [backup] config.
			// Cancelled when serve's signal-bound context fires
			// alongside the HTTP server's graceful shutdown.
			schedCtx, schedCancel := context.WithCancel(cmd.Context())
			defer schedCancel()
			go backup.Schedule(schedCtx, backup.Options{
				DSN:       cfg.DB.DSN,
				Dir:       cfg.Backup.Dir,
				Cadence:   cfg.Backup.Cadence,
				Retention: cfg.Backup.Retention,
				Logger:    logger,
			})

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
				logger.Info("shutting down", "op", "shutdown", "grace", cfg.Server.ShutdownTimeout.String())
				shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
				defer cancel()
				if err := httpSrv.Shutdown(shutdownCtx); err != nil {
					return fmt.Errorf("shutdown: %w", err)
				}
			}
			return nil
		},
	}
	cmdhelp.SetRelated(cmd,
		"rex-central backup --config /etc/rex/central.toml --output /var/backups/rex",
		"rex-central restore --config /etc/rex/central.toml --from /var/backups/rex.dump",
		"rex-central --help",
	)
	cmd.Flags().StringVar(&configPath, "config", "", "path to central.toml (default: /etc/rex/central.toml; missing file is OK)")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "TCP address to listen on (overrides config + REX_CENTRAL_ADDR)")
	cmd.Flags().DurationVar(&shutdownTimeout, "shutdown-timeout", 15*time.Second, "max wait for graceful shutdown (overrides config + REX_CENTRAL_SHUTDOWN_TIMEOUT)")
	cmd.Flags().StringVar(&keysFile, "keys", "", "path to authorized-keys TOML file (overrides config + REX_CENTRAL_KEYS_FILE)")
	cmd.Flags().StringVar(&dbDSN, "db", "", "Postgres DSN (overrides config + REX_CENTRAL_DB_DSN); empty uses in-memory store")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level: debug | info | warn | error (overrides config + REX_CENTRAL_LOG_LEVEL)")
	cmd.Flags().StringVar(&logFormat, "log-format", "json", "log format: json | text (overrides config + REX_CENTRAL_LOG_FORMAT)")
	cmd.Flags().BoolVar(&webEnabled, "web", false, "enable the central web UI shell (off by default until read-side pages land; serves /_web/health + /static/ when on)")
	cmd.Flags().BoolVar(&devMode, "dev", false, "developer-convenience mode: register the local rex default identity as an authorized key, promote it to admin of the default org (requires --db), and auto-enable --web. NEVER use in production.")
	cmd.Flags().StringVar(&devIdentityDir, "dev-identity-dir", "", "override identity store path used by --dev (default: identity.DefaultStoreDir() — same dir `rex identity show` uses)")
	return cmd
}

// loadConfigForCommand resolves the same defaults < file < env <
// flags precedence the serve command uses, but for one-shot
// subcommands that only need a subset of the config (DSN +
// backup dir, typically). Centralized so backup and restore
// don't duplicate the resolve dance.
func loadConfigForCommand(cmd *cobra.Command, configPath string) (config.Config, error) {
	cfgPath := config.PathOrDefault(configPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return cfg, err
	}
	cfg.Resolve(cfgPath)
	return cfg, nil
}

func newBackupCmd() *cobra.Command {
	var (
		configPath string
		outputDir  string
		dsn        string
	)
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Run a one-shot pg_dump against the configured database",
		Long: `Writes a single Postgres dump to the configured backup
directory (or --output) and exits. Useful for ad-hoc snapshots
and CI smoke tests; the scheduled cadence runs only when serve
is alive (BACKUP.1).

Honours the same defaults < /etc/rex/central.toml < REX_CENTRAL_*
env vars < CLI flags precedence as serve. The DSN must point at
a reachable Postgres; pg_dump must be on PATH (the bundled image
ships postgresql-client; bare-metal deployments must install it).
`,
		Example: `  rex-central backup --config /etc/rex/central.toml --output /var/backups/rex
  rex-central backup --db 'postgres://user:pass@host/db' --output /tmp/rex-backups`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForCommand(cmd, configPath)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("db") {
				cfg.DB.DSN = dsn
			}
			if cmd.Flags().Changed("output") {
				cfg.Backup.Dir = outputDir
			}
			if cfg.DB.DSN == "" {
				return fmt.Errorf("backup: db dsn is required (set --db or db.dsn / REX_CENTRAL_DB_DSN)")
			}
			if cfg.Backup.Dir == "" {
				return fmt.Errorf("backup: output dir is required (set --output or backup.dir / REX_CENTRAL_BACKUP_DIR)")
			}
			path, took, err := backup.Run(cmd.Context(), backup.Options{
				DSN:       cfg.DB.DSN,
				Dir:       cfg.Backup.Dir,
				Retention: cfg.Backup.Retention,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s in %s\n", path, took)
			return nil
		},
	}
	cmdhelp.SetRelated(cmd,
		"rex-central serve --config /etc/rex/central.toml",
		"rex-central restore --from /path/to/rex.dump",
	)
	cmd.Flags().StringVar(&configPath, "config", "", "path to central.toml (default: /etc/rex/central.toml; missing file is OK)")
	cmd.Flags().StringVar(&outputDir, "output", "", "directory to write the dump into (overrides backup.dir)")
	cmd.Flags().StringVar(&dsn, "db", "", "Postgres DSN (overrides db.dsn / REX_CENTRAL_DB_DSN)")
	return cmd
}

func newRestoreCmd() *cobra.Command {
	var (
		configPath string
		fromPath   string
		dsn        string
	)
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a Postgres dump produced by `rex-central backup`",
		Long: `Validates the dump file (BACKUP.3 — checks the PGDMP magic
header and that pg_restore can list the contents-of-table) and
applies it to the configured database with --clean --if-exists.

Recommended workflow:

  docker compose down
  rex-central restore --from /backups/rex-central-20260504T120000Z.dump
  docker compose up -d

The destructive nature of --clean is intentional: a restore
overwrites the existing schema with what the dump contained.
Run against an empty database when in doubt.
`,
		Example: `  rex-central restore --config /etc/rex/central.toml --from /var/backups/rex.dump
  rex-central restore --db 'postgres://user:pass@host/db' --from /tmp/rex.dump`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForCommand(cmd, configPath)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("db") {
				cfg.DB.DSN = dsn
			}
			if cfg.DB.DSN == "" {
				return fmt.Errorf("restore: db dsn is required (set --db or db.dsn / REX_CENTRAL_DB_DSN)")
			}
			if fromPath == "" {
				return fmt.Errorf("restore: --from <path> is required")
			}
			if err := backup.Restore(cmd.Context(), cfg.DB.DSN, fromPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored from %s\n", fromPath)
			return nil
		},
	}
	cmdhelp.SetRelated(cmd,
		"rex-central backup --config /etc/rex/central.toml --output /var/backups/rex",
		"rex-central serve --config /etc/rex/central.toml",
	)
	cmd.Flags().StringVar(&configPath, "config", "", "path to central.toml (default: /etc/rex/central.toml; missing file is OK)")
	cmd.Flags().StringVar(&fromPath, "from", "", "path to the dump file produced by `rex-central backup`")
	cmd.Flags().StringVar(&dsn, "db", "", "Postgres DSN (overrides db.dsn / REX_CENTRAL_DB_DSN)")
	if err := cmd.MarkFlagRequired("from"); err != nil {
		// MarkFlagRequired only errors if the flag is not
		// registered; we just registered it. Keeps cobra happy
		// without panicking.
		_ = err
	}
	return cmd
}
