package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	// claudeSetupTokenProbeModel is the cheapest model used to validate a setup token.
	claudeSetupTokenProbeModel = "claude-haiku-4-5-20251001"
	// claudeSetupTokenProbeMinGap stops repeated manual/forced probes from burning quota.
	claudeSetupTokenProbeMinGap  = 60 * time.Second
	claudeSetupTokenProbeTimeout = 30 * time.Second
	claudeSetupTokenProbeAtKey   = "setup_token_probe_at"
	claudeSetupTokenProbeStatus  = "setup_token_probe_status"
)

// claudeSetupTokenProber sends a minimal request with auth and returns the
// upstream HTTP status (0 when the request did not reach upstream) and error.
type claudeSetupTokenProber func(context.Context, *cliproxyauth.Auth) (int, http.Header, error)

// setupTokenProbeError is returned when the probe proves the token is invalid.
type setupTokenProbeError struct {
	code     int
	upstream int
	body     string
	msg      string
}

func (e setupTokenProbeError) Error() string   { return e.msg }
func (e setupTokenProbeError) StatusCode() int { return e.code }

// UpstreamStatus is the HTTP status Anthropic returned to the probe.
func (e setupTokenProbeError) UpstreamStatus() int { return e.upstream }

// UpstreamBody is Anthropic's raw error body for the probe.
func (e setupTokenProbeError) UpstreamBody() string { return e.body }

func (e *ClaudeExecutor) probeSetupToken(ctx context.Context, auth *cliproxyauth.Auth) (int, http.Header, error) {
	if e.setupTokenProber != nil {
		return e.setupTokenProber(ctx, auth)
	}
	probeCtx, cancel := context.WithTimeout(internallogging.WithFreshResponseHeadersHolder(ctx), claudeSetupTokenProbeTimeout)
	defer cancel()
	payload := []byte(`{"model":"` + claudeSetupTokenProbeModel + `","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	_, err := e.Execute(probeCtx, auth, cliproxyexecutor.Request{
		Model:   claudeSetupTokenProbeModel,
		Payload: payload,
		Format:  sdktranslator.FromString("claude"),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: payload,
	})
	headers := internallogging.GetResponseHeaders(probeCtx)
	if err == nil {
		return http.StatusOK, headers, nil
	}
	var sc interface{ StatusCode() int }
	if errors.As(err, &sc) && sc != nil {
		return sc.StatusCode(), headers, err
	}
	return 0, headers, err
}

// refreshSetupTokenByProbe is the setup-token equivalent of an OAuth refresh:
// a 1-token messages request proves the token still works and stamps
// last_refresh; 401/403 marks it unauthorized; other failures retry later.
func (e *ClaudeExecutor) refreshSetupTokenByProbe(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	now := time.Now()
	if ts := claudeauth.ReadMetadataString(&auth.Metadata, claudeSetupTokenProbeAtKey); ts != "" {
		if last, err := time.Parse(time.RFC3339, ts); err == nil && now.Sub(last) < claudeSetupTokenProbeMinGap {
			log.Debugf("claude executor: setup-token probe for %s skipped (last probe %s ago)", auth.ID, now.Sub(last).Round(time.Second))
			return auth, nil
		}
	}
	status, headers, err := e.probeSetupToken(ctx, auth)
	if errCtx := ctx.Err(); errCtx != nil && err != nil {
		return nil, errCtx
	}
	claudeauth.EnsureMetadataMap(&auth.Metadata)
	claudeauth.StoreMetadataValue(&auth.Metadata, claudeSetupTokenProbeAtKey, now.UTC().Format(time.RFC3339))
	claudeauth.StoreMetadataValue(&auth.Metadata, claudeSetupTokenProbeStatus, status)
	switch {
	case err == nil, status == http.StatusTooManyRequests:
		// 429 means the token authenticated but the account is rate limited.
		claudeauth.StoreMetadataValue(&auth.Metadata, "last_refresh", now.Format(time.RFC3339))
		cliproxyauth.RecordClaudeSetupTokenProbeHeaders(auth, headers, now)
		log.Infof("claude executor: setup-token probe ok for %s (status %d)", auth.ID, status)
		return auth, nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		log.Warnf("claude executor: setup-token probe rejected for %s (status %d)", auth.ID, status)
		body := ""
		if err != nil {
			body = err.Error()
		}
		return nil, setupTokenProbeError{code: http.StatusUnauthorized, upstream: status, body: body, msg: fmt.Sprintf("claude setup-token probe rejected: status %d: %v", status, err)}
	default:
		return nil, fmt.Errorf("claude setup-token probe failed (status %d): %w", status, err)
	}
}
