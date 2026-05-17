package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// newRemoteLoginCmd implements `rex remote login` — the CLI side
// of the browser-login flow (web-ui.CENTRAL-AUTH.2). The user
// lands on `<central>/login`, copies the printed challenge string
// into their terminal, and this command signs it with the local
// keypair, exchanges the signature for an access token, and either
// opens the browser at `<central>/auth/redeem?token=…` (desktop
// default) or prints the redeem URL (--print-url, the headless
// fallback).
//
// --challenge is optional. When provided it carries the full
// base64-encoded LoginChallengePackage that the /login page
// rendered, including the post-login redirect path. When omitted
// the CLI does its own /auth/challenge handshake and the browser
// lands at "/" (single-org users land on /orgs/<id>, multi-org
// users on a picker; both choices live with the read-side pages).
func newRemoteLoginCmd() *cobra.Command {
	var (
		challenge string
		printURL  bool
		scope     string
		redirect  string
	)
	cmd := &cobra.Command{
		Use:   "login <name> [url]",
		Short: "Authenticate a browser session against a remote central node",
		Long: `Signs the central's /login challenge with the local keypair and
opens (or prints) the redeem URL that sets the browser session
cookie. Companion to the /login page on the central web UI.

Run rex remote login from a terminal that holds your private key:

  rex remote login primary --challenge "<copied-from-/login>"

Without --challenge, the CLI fetches a fresh challenge itself and
the browser is sent to "/" on the central (the post-login
landing page; central-read-side-pages handles the org/workspace
picker from there).

Two arg forms:

  rex remote login <name>            uses the URL stored in
                                     ~/.config/rex/remotes.toml
                                     for <name>.
  rex remote login <name> <url>      one-shot mode: uses <url>
                                     directly without touching
                                     the registry. Handy for
                                     ` + "`make web-dev`" + ` where the
                                     remote hasn't been added
                                     yet — sign in once to
                                     verify, then run
                                     ` + "`rex remote add`" + ` if you
                                     want persistence.`,
		Example: `  rex remote login primary --challenge "$(pbpaste)"
  rex remote login dev http://127.0.0.1:8080
  rex remote login primary --challenge "<token>" --print-url
  rex remote login primary --redirect /orgs/acme`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			var remoteURL string
			if len(args) == 2 {
				// Inline-URL form: skip the registry lookup so the
				// user can sign in to a freshly-launched dev
				// central without `rex remote add` first.
				remoteURL = args[1]
			} else {
				reg, _, err := loadRegistry(cmd)
				if err != nil {
					return err
				}
				r, ok := reg.Get(name)
				if !ok {
					return fmt.Errorf("remote %q not registered (pass the URL as a second arg to skip the registry: `rex remote login %s <url>`)", name, name)
				}
				remoteURL = r.URL
			}

			signer, err := loadOrCreateDefaultSigner(cmd)
			if err != nil {
				return fmt.Errorf("login %q: %w", name, err)
			}

			ctx := commandContext(cmd)
			result, err := runRemoteLogin(ctx, remoteURL, signer, challenge, redirect, scope, http.DefaultClient)
			if err != nil {
				return fmt.Errorf("login %q: %w", name, err)
			}

			out := cmd.OutOrStdout()
			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"name":        name,
					"url":         remoteURL,
					"redeem_url":  result.RedeemURL,
					"fingerprint": signer.Fingerprint().String(),
					"expires_at":  result.AccessTokenExpiresAt,
					"opened":      result.Opened,
				})
			}

			if printURL || !openBrowser(result.RedeemURL) {
				result.Opened = false
				fmt.Fprintf(out,
					"%s OK — open this URL in your browser to complete sign-in:\n  %s\n",
					name, result.RedeemURL)
			} else {
				result.Opened = true
				fmt.Fprintf(out,
					"%s OK — opened the redeem URL in your browser.\n",
					name)
			}
			return nil
		},
	}
	setRelated(cmd,
		"rex remote test <name>",
		"rex remote show <name>",
		"rex identity show --pub",
	)
	cmd.Flags().StringVar(&challenge, "challenge", "", "base64 challenge package copied from <central>/login (optional)")
	cmd.Flags().BoolVar(&printURL, "print-url", false, "always print the redeem URL instead of trying to open a browser")
	cmd.Flags().StringVar(&scope, "scope", "sync", "token scope to request from the central (default: sync)")
	cmd.Flags().StringVar(&redirect, "redirect", "", "post-login redirect path; overrides the path embedded in --challenge")
	addRemoteSharedFlags(cmd)
	return cmd
}

// loginResult bundles the side-effect data the command surfaces.
type loginResult struct {
	RedeemURL            string
	AccessTokenExpiresAt time.Time
	Opened               bool
}

// runRemoteLogin is the side-effect-free core of `rex remote
// login`: it runs the challenge handshake and assembles the
// browser redeem URL without touching stdout or os/exec. Pulled
// out so the cobra wrapper stays focused on flag parsing and the
// tests stay focused on flow logic.
//
// When challengeWire is empty, the function calls POST
// /auth/challenge to mint a fresh challenge. When provided it
// decodes the package and uses its fields directly. Explicit
// redirectOverride wins over the package's Redirect.
func runRemoteLogin(
	ctx context.Context,
	baseURL string,
	signer identity.Signer,
	challengeWire string,
	redirectOverride string,
	scope string,
	hc *http.Client,
) (loginResult, error) {
	if scope == "" {
		scope = "sync"
	}
	if hc == nil {
		hc = http.DefaultClient
	}

	// Fail fast on an obviously-bad redirect override before
	// touching the server — keeps an attacker-supplied open-redirect
	// from racing the handshake.
	if redirectOverride != "" && !strings.HasPrefix(redirectOverride, "/") {
		return loginResult{}, fmt.Errorf("redirect %q is not a same-origin path", redirectOverride)
	}

	var pkg proto.LoginChallengePackage
	if challengeWire != "" {
		decoded, err := proto.DecodeLoginChallengePackage(challengeWire)
		if err != nil {
			return loginResult{}, err
		}
		pkg = decoded
		if !pkg.ExpiresAt.IsZero() && time.Now().After(pkg.ExpiresAt) {
			return loginResult{}, fmt.Errorf("challenge expired at %s", pkg.ExpiresAt.Format(time.RFC3339))
		}
	} else {
		freshPkg, err := fetchFreshChallenge(ctx, baseURL, hc)
		if err != nil {
			return loginResult{}, err
		}
		pkg = freshPkg
	}

	nonceBytes, err := hex.DecodeString(pkg.Nonce)
	if err != nil {
		return loginResult{}, fmt.Errorf("decode nonce: %w", err)
	}
	canonical, err := json.Marshal(proto.ChallengeSigningInput{
		Version:  proto.AuthSigningVersion,
		Nonce:    hex.EncodeToString(nonceBytes),
		Hostname: pkg.Hostname,
		Scope:    scope,
	})
	if err != nil {
		return loginResult{}, fmt.Errorf("marshal signing input: %w", err)
	}
	sig, err := signer.Sign(ctx, canonical)
	if err != nil {
		return loginResult{}, fmt.Errorf("sign challenge: %w", err)
	}

	verifyBody, err := json.Marshal(proto.AuthVerifyRequest{
		ChallengeID: pkg.ChallengeID,
		Fingerprint: signer.Fingerprint().String(),
		Scope:       scope,
		Signature:   hex.EncodeToString(sig),
	})
	if err != nil {
		return loginResult{}, fmt.Errorf("marshal verify: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/verify",
		bytes.NewReader(verifyBody))
	if err != nil {
		return loginResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return loginResult{}, fmt.Errorf("POST /auth/verify: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return loginResult{}, fmt.Errorf("/auth/verify: %s — %s", resp.Status, bytes.TrimSpace(body))
	}
	var verifyRes proto.AuthVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&verifyRes); err != nil {
		return loginResult{}, fmt.Errorf("decode verify response: %w", err)
	}

	redirect := pkg.Redirect
	if redirectOverride != "" {
		redirect = redirectOverride
	}
	if redirect == "" {
		redirect = "/"
	}
	if !strings.HasPrefix(redirect, "/") {
		return loginResult{}, fmt.Errorf("redirect %q is not a same-origin path", redirect)
	}

	redeemURL, err := buildRedeemURL(baseURL, verifyRes.AccessToken, redirect)
	if err != nil {
		return loginResult{}, err
	}
	return loginResult{
		RedeemURL:            redeemURL,
		AccessTokenExpiresAt: verifyRes.ExpiresAt,
	}, nil
}

// fetchFreshChallenge POSTs /auth/challenge and shapes the
// response into a LoginChallengePackage. Used when --challenge is
// omitted; the returned package has no Redirect so the caller's
// redirect (or the default "/") wins.
func fetchFreshChallenge(ctx context.Context, baseURL string, hc *http.Client) (proto.LoginChallengePackage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/challenge", http.NoBody)
	if err != nil {
		return proto.LoginChallengePackage{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return proto.LoginChallengePackage{}, fmt.Errorf("POST /auth/challenge: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return proto.LoginChallengePackage{}, fmt.Errorf("/auth/challenge: %s — %s", resp.Status, bytes.TrimSpace(body))
	}
	var ch proto.AuthChallengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return proto.LoginChallengePackage{}, fmt.Errorf("decode challenge: %w", err)
	}
	return proto.LoginChallengePackage{
		Version:     proto.LoginChallengePackageVersion,
		ChallengeID: ch.ChallengeID,
		Nonce:       ch.Nonce,
		Hostname:    ch.Hostname,
		ExpiresAt:   ch.ExpiresAt,
	}, nil
}

// buildRedeemURL joins baseURL + /auth/redeem with the token and
// redirect query parameters URL-encoded. Returns an error when
// baseURL is malformed so the user sees a clear diagnostic before
// the browser opens.
func buildRedeemURL(baseURL, token, redirect string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("base url must include scheme and host")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/auth/redeem"
	q := u.Query()
	q.Set("token", token)
	q.Set("redirect", redirect)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// openBrowser tries to launch the system browser at url. Returns
// true on success, false otherwise; callers fall back to printing
// the URL. The function never errors — a missing helper binary or
// a sandboxed environment is a UX hint, not a CLI failure.
//
// Overridable for tests via the package-level variable below.
var openBrowser = func(rawURL string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default: // linux, freebsd, etc.
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start() == nil
}
