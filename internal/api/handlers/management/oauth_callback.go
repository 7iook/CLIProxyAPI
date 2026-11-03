package management

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	log "github.com/sirupsen/logrus"
)

type oauthCallbackRequest struct {
	Provider    string `json:"provider"`
	RedirectURL string `json:"redirect_url"`
	Code        string `json:"code"`
	State       string `json:"state"`
	Error       string `json:"error"`
}

func (h *Handler) PostOAuthCallback(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "handler not initialized"})
		return
	}

	var req oauthCallbackRequest
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errBindJSON, http.StatusBadRequest), gin.H{"status": "error", "error": "invalid body"})
		return
	}
	h.handleOAuthCallback(c, req)
}

func (h *Handler) GetOAuthCallback(c *gin.Context) {
	req := oauthCallbackRequest{
		Provider: strings.TrimSpace(c.Query("provider")),
		Code:     strings.TrimSpace(c.Query("code")),
		State:    strings.TrimSpace(c.Query("state")),
		Error:    firstNonEmpty(c.Query("error"), c.Query("error_description")),
	}
	h.handleOAuthCallback(c, req)
}

func (h *Handler) handleOAuthCallback(c *gin.Context, req oauthCallbackRequest) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "handler not initialized"})
		return
	}

	state := strings.TrimSpace(req.State)
	code := strings.TrimSpace(req.Code)
	errMsg := strings.TrimSpace(req.Error)

	if rawRedirect := strings.TrimSpace(req.RedirectURL); rawRedirect != "" && !isHTTPCallbackURL(rawRedirect) {
		// Manual copy-paste input (e.g. the Claude code page shows "code#state"):
		// treat a non-URL redirect_url as the code itself.
		if code == "" {
			code = rawRedirect
		}
	} else if rawRedirect != "" {
		u, errParse := url.Parse(rawRedirect)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid redirect_url"})
			return
		}
		q := u.Query()
		if state == "" {
			state = strings.TrimSpace(q.Get("state"))
		}
		if code == "" {
			code = strings.TrimSpace(q.Get("code"))
		}
		if errMsg == "" {
			errMsg = strings.TrimSpace(q.Get("error"))
			if errMsg == "" {
				errMsg = strings.TrimSpace(q.Get("error_description"))
			}
		}
	}

	// Claude returns the code as "code#state"; split it so a pasted code still
	// resolves its session. A fragment state must agree with any explicit state.
	if idx := strings.Index(code, "#"); idx >= 0 {
		fragmentState := strings.TrimSpace(code[idx+1:])
		code = strings.TrimSpace(code[:idx])
		if fragmentState != "" {
			if state == "" {
				state = fragmentState
			} else if fragmentState != state {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "state does not match code"})
				return
			}
		}
	}

	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "state is required"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}
	if code == "" && errMsg == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "code or error is required"})
		return
	}

	sessionProvider, sessionStatus, isPlugin, _, completed, ok := GetOAuthSessionDetails(state)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if completed {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "oauth flow is already completed"})
		return
	}
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = sessionProvider
	}
	var canonicalProvider string
	var errNormalize error
	if isPlugin {
		canonicalProvider, errNormalize = NormalizePluginOAuthCallbackProvider(provider)
	} else {
		canonicalProvider, errNormalize = NormalizeOAuthCallbackProvider(provider)
	}
	if errNormalize != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "unsupported provider"})
		return
	}
	if sessionStatus != "" {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": sessionStatus})
		return
	}
	if !strings.EqualFold(sessionProvider, canonicalProvider) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "provider does not match state"})
		return
	}

	if _, errWrite := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, canonicalProvider, state, code, errMsg); errWrite != nil {
		if errors.Is(errWrite, errOAuthSessionNotPending) {
			_, status, okSession := GetOAuthSession(state)
			if okSession && status != "" {
				c.JSON(http.StatusConflict, gin.H{"status": "error", "error": status})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "oauth flow is not pending"})
			return
		}
		log.WithError(errWrite).Error("failed to persist oauth callback")
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to persist oauth callback"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// isHTTPCallbackURL reports whether a pasted callback is an absolute http(s)
// URL rather than a bare authorization code.
func isHTTPCallbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		// Keep the historic "invalid redirect_url" answer for broken URLs.
		lower := strings.ToLower(raw)
		return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
