package cli

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/local/web"
)

// newServeCmd returns `rex serve` — the local web UI server. Loopback
// default per web-ui.LOCAL.2; remote-network exposure requires
// explicit --addr beyond 127.0.0.1.
func newServeCmd(version string) *cobra.Command {
	var (
		addr            string
		shutdownTimeout time.Duration
		workspaceFlag   string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local web UI",
		Long: `Starts the local web UI on --addr (default 127.0.0.1:7474). Honours
SIGINT and SIGTERM with a graceful shutdown of up to
--shutdown-timeout (default 15s) before forcing exit.

The server binds to loopback by default. Listening on a non-loopback
address is opt-in: pass --addr 0.0.0.0:<port> (or similar) and
acknowledge that local-machine identity is the trust model — every
request is treated as the workspace owner.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootForOrError(workspaceFlag)
			if err != nil {
				return err
			}

			s, err := web.New(web.Options{
				WorkspaceRoot: root,
				BindAddr:      addr,
				Version:       version,
			})
			if err != nil {
				return fmt.Errorf("build server: %w", err)
			}

			srv := &http.Server{
				Addr:              addr,
				Handler:           s.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"rex local UI serving on http://%s/  (workspace=%s)\n",
				addr, root)

			errCh := make(chan error, 1)
			go func() {
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					errCh <- err
				}
				close(errCh)
			}()

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
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
				if err := srv.Shutdown(shutdownCtx); err != nil {
					return fmt.Errorf("shutdown: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7474", "TCP address to bind (loopback default)")
	cmd.Flags().DurationVar(&shutdownTimeout, "shutdown-timeout", 15*time.Second, "max wait for graceful shutdown")
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root to serve (default: walk up from cwd)")
	return cmd
}
