package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/asabla/rex/internal/core/sync/proto"
	internalweb "github.com/asabla/rex/internal/web"
)

// wantsJSON reports whether r's Accept header asks for JSON.
// Used by the invite handlers so the same routes serve both the
// browser-facing HTML form and the CLI-facing JSON API
// (`rex remote join --invite ...`).
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// writeJSONError emits {"error": "..."} with the supplied
// status code. Used by the invite JSON path so the CLI can
// surface the server's reason without HTML scraping.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleInvitePeek is GET /invites/<token>. Renders a
// paste-your-public-key form pre-stamped with the token, or a
// state-specific error page when the token is unknown / expired
// / already redeemed (web-ui surface for
// identity-and-trust.AUTH.2.1).
//
// Unauthenticated: the invite token is the credential. The route
// is listed in isPublicWebPath so the session gate doesn't bounce
// recipients to /login on the way in.
//
// 503 when the InviteRedeemer is unbound (the dev/test deployment
// path where --keys + --db aren't wired). Production setups bind
// the adapter from cmd/rex-central.
func (s *Server) handleInvitePeek(w http.ResponseWriter, r *http.Request) {
	if s.opts.Redeemer == nil {
		http.Error(w, "central web: invite redeem not configured (requires --db)", http.StatusServiceUnavailable)
		return
	}
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	inv, err := s.opts.Redeemer.PeekInvite(token)
	if err != nil {
		if wantsJSON(r) {
			writeInviteErrorJSON(w, err)
			return
		}
		writeInviteErrorPage(w, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, proto.PeekInviteResponse{
			Token:     inv.Token,
			OrgID:     inv.OrgID,
			Role:      inv.Role,
			InvitedBy: inv.InvitedBy,
			ExpiresAt: inv.ExpiresAt.UTC().Format(time.RFC3339),
		})
		return
	}
	writeInviteFormPage(w, inv, "")
}

// writeJSON is the json.NewEncoder boilerplate centralised.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeInviteErrorJSON mirrors writeInviteErrorPage but emits
// JSON + the same status-code mapping (404 / 410 / 409 / 400).
func writeInviteErrorJSON(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, internalweb.ErrInviteNotFound):
		status = http.StatusNotFound
	case errors.Is(err, internalweb.ErrInviteExpired):
		status = http.StatusGone
	case errors.Is(err, internalweb.ErrInviteAlreadyRedeemed):
		status = http.StatusConflict
	}
	writeJSONError(w, status, err.Error())
}

// handleInviteRedeem is POST /invites/redeem. Parses the form,
// calls the redeemer (which under the hood writes the
// authorized_keys + org_memberships rows, marks the invite
// redeemed, overlays the new key into the in-memory Keystore, and
// emits the audit pair), and renders the success page.
//
// On error the form is re-rendered with the failure message
// inline so the recipient can correct the input (typo in PEM,
// expired in the meantime, etc) without losing their place.
func (s *Server) handleInviteRedeem(w http.ResponseWriter, r *http.Request) {
	if s.opts.Redeemer == nil {
		http.Error(w, "central web: invite redeem not configured (requires --db)", http.StatusServiceUnavailable)
		return
	}
	wantJSON := wantsJSON(r) || strings.Contains(r.Header.Get("Content-Type"), "application/json")
	var (
		token, handle, pem string
	)
	if wantJSON {
		var body proto.RedeemInviteRequest
		if err := decodeJSONBody(r, &body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		token = strings.TrimSpace(body.Token)
		handle = strings.TrimSpace(body.Handle)
		pem = strings.TrimSpace(body.PublicKeyPEM)
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "central web: parse form: "+err.Error(), http.StatusBadRequest)
			return
		}
		token = strings.TrimSpace(r.FormValue("token"))
		handle = strings.TrimSpace(r.FormValue("handle"))
		pem = strings.TrimSpace(r.FormValue("public_key_pem"))
	}
	if token == "" || pem == "" {
		if wantJSON {
			writeJSONError(w, http.StatusBadRequest, "token and public_key_pem are required")
			return
		}
		http.Error(w, "central web: token and public_key_pem are required", http.StatusBadRequest)
		return
	}
	out, err := s.opts.Redeemer.RedeemInvite(internalweb.RedeemRequest{
		Token:        token,
		Handle:       handle,
		PublicKeyPEM: pem,
	})
	if err != nil {
		// Sentinel errors render the error page (or JSON);
		// everything else re-renders the form with an inline
		// message so a PEM typo doesn't force the user to chase
		// a new invite link.
		switch {
		case errors.Is(err, internalweb.ErrInviteNotFound),
			errors.Is(err, internalweb.ErrInviteExpired),
			errors.Is(err, internalweb.ErrInviteAlreadyRedeemed):
			if wantJSON {
				writeInviteErrorJSON(w, err)
				return
			}
			writeInviteErrorPage(w, err)
			return
		default:
			if wantJSON {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			// Re-render the form with the original invite
			// summary so the recipient sees the org/role they
			// were redeeming for, alongside the error. PeekInvite
			// might fail too (the invite may have lapsed
			// between submit + re-peek); fall back to a bare
			// form in that case.
			inv, peekErr := s.opts.Redeemer.PeekInvite(token)
			if peekErr != nil {
				writeInviteErrorPage(w, peekErr)
				return
			}
			writeInviteFormPage(w, inv, err.Error())
			return
		}
	}
	if wantJSON {
		writeJSON(w, http.StatusOK, proto.RedeemInviteResponse{
			OrgID:       out.OrgID,
			Fingerprint: out.Fingerprint,
			Role:        out.Role,
		})
		return
	}
	writeInviteSuccessPage(w, out)
}

// decodeJSONBody parses r.Body into v with a tight 64 KiB cap
// (the redeem payload is short — a token + handle + PEM block).
// Wraps json.Decoder so a stray trailing object surfaces as an
// error rather than silently parsing the first one.
func decodeJSONBody(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 64*1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// writeInviteFormPage renders the GET /invites/<token> body.
// errMsg renders as a banner above the form when non-empty (used
// after a failed POST). The token is stashed on the form as a
// hidden input so the recipient doesn't need to re-paste it.
func writeInviteFormPage(w http.ResponseWriter, inv internalweb.InviteSummary, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	errorBanner := ""
	if errMsg != "" {
		errorBanner = `<aside class="banner banner-error" role="alert"><strong>Could not redeem:</strong> ` +
			html.EscapeString(errMsg) + `</aside>`
	}
	body := `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>rex-central — accept invite</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
  <main class="login">
    <h1>Accept invite</h1>
    <p>An admin issued this invite. Joining as
       <strong>` + html.EscapeString(inv.Role) + `</strong> in org
       <code>` + html.EscapeString(inv.OrgID) + `</code>. The invite expires at
       <time>` + html.EscapeString(inv.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")) + `</time>.</p>
    ` + errorBanner + `
    <form method="post" action="/invites/redeem" class="invite-redeem">
      <input type="hidden" name="token" value="` + html.EscapeString(inv.Token) + `">
      <label>
        handle (optional, for display only):
        <input type="text" name="handle" placeholder="alice">
      </label>
      <label>
        your public key (PEM, generate with <code>rex identity export --public</code>):
        <textarea name="public_key_pem" rows="6" required
          placeholder="-----BEGIN PUBLIC KEY-----..."></textarea>
      </label>
      <button class="btn btn-primary" type="submit">accept invite</button>
    </form>
    <p class="login-meta">Your key is registered with this central node only.
       The private half never leaves your machine.</p>
  </main>
</body>
</html>
`
	_, _ = fmt.Fprint(w, body)
}

// writeInviteErrorPage renders the bad-token / expired /
// already-redeemed page. Status code is mapped from the sentinel
// so monitoring can distinguish them; the body intentionally
// avoids leaking which condition tripped (the recipient sees the
// same "this link is not valid" message regardless).
func writeInviteErrorPage(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, internalweb.ErrInviteNotFound):
		status = http.StatusNotFound
	case errors.Is(err, internalweb.ErrInviteExpired):
		status = http.StatusGone
	case errors.Is(err, internalweb.ErrInviteAlreadyRedeemed):
		status = http.StatusConflict
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	body := `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>rex-central — invite not redeemable</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
  <main class="login">
    <h1>This invite link is not valid</h1>
    <p>The token is unknown, expired, or already redeemed. Ask the admin
       who issued it for a fresh invite.</p>
  </main>
</body>
</html>
`
	_, _ = fmt.Fprint(w, body)
}

// writeInviteSuccessPage renders the post-redeem confirmation.
// The recipient sees the org/role they joined + the registered
// fingerprint (so they can confirm the correct key landed). A
// nudge to /login closes the loop: the next step is to start a
// session with the freshly-registered key.
func writeInviteSuccessPage(w http.ResponseWriter, out internalweb.RedeemOutcome) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>rex-central — invite redeemed</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
  <main class="login">
    <h1>Invite redeemed</h1>
    <p>You joined org <code>` + html.EscapeString(out.OrgID) + `</code> as
       <strong>` + html.EscapeString(out.Role) + `</strong>. Your key fingerprint:
       <code>` + html.EscapeString(out.Fingerprint) + `</code>.</p>
    <p>Next step: <a href="/login">sign in</a> from a terminal that holds the
       matching private key.</p>
  </main>
</body>
</html>
`
	_, _ = fmt.Fprint(w, body)
}
