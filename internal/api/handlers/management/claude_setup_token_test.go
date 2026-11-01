package management

import (
	"context"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	helps "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestSetupTokenAccountSeedMatchesExecutor locks the handler's empty-identity
// account seed to the executor PrepareRequestAuth fallback. It runs the REAL
// executor over a setup-token credential whose ID/Index/FileName are empty
// (forcing the executor onto its "claude-setup-token|"+token seed branch, the
// same branch the handler mirrors), then asserts the executor-derived
// account_uuid equals the handler's own fallback expression. Each side reaches
// its value independently: the handler side evaluates
// claudeSetupTokenAccountSeedString (the actual production expression) while the
// executor side is the executor's real code path. A change to either side's
// seed literal breaks the equality, which a both-sides-inlined literal could
// not detect.
func TestSetupTokenAccountSeedMatchesExecutor(t *testing.T) {
	token := "sk-ant-oat01-example-token"

	// Handler side: the exact expression RequestClaudeSetupToken uses for the
	// empty-identity fallback.
	handlerAccountUUID := helps.StableClaudeCLIAccountUUID(claudeSetupTokenAccountSeedString(token))

	// Executor side: run the real PrepareRequestAuth. Empty ID/Index/FileName
	// force ClaudeCLIAuthIdentitySeed to return "" so the executor falls back to
	// its "claude-setup-token|"+apiKey seed, the branch the handler mirrors.
	auth := &coreauth.Auth{
		Provider:   "claude",
		Attributes: map[string]string{"api_key": token},
		Metadata: map[string]any{
			"access_token":   token,
			"is_setup_token": true,
		},
	}
	ex := executor.NewClaudeExecutor(&config.Config{})
	prepared, err := ex.PrepareRequestAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", err)
	}
	executorAccountUUID := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid")

	if executorAccountUUID == "" {
		t.Fatal("executor did not derive a fallback account_uuid")
	}
	if handlerAccountUUID != executorAccountUUID {
		t.Fatalf("handler account_uuid %q != executor account_uuid %q; the two seed paths have diverged", handlerAccountUUID, executorAccountUUID)
	}
}

// TestClaudeSetupTokenCredentialRecognized verifies the metadata a setup-token
// login persists is recognised as a setup token by the runtime classifier.
func TestClaudeSetupTokenCredentialRecognized(t *testing.T) {
	token := "sk-ant-oat01-example"
	auth := &coreauth.Auth{
		Provider: "claude",
		Metadata: map[string]any{
			"access_token":   token,
			"is_setup_token": true,
			"auth_kind":      "setup_token",
		},
	}
	if !coreauth.IsClaudeSetupTokenCredential(auth, token) {
		t.Fatal("setup-token credential must be recognised via is_setup_token metadata")
	}
}

// TestNormalOAuthClearsSetupTokenMark verifies a normal OAuth login that
// overwrites a prior setup-token file for the same identity clears the stale
// is_setup_token marker instead of inheriting it through metadata merge.
func TestNormalOAuthClearsSetupTokenMark(t *testing.T) {
	token := "sk-ant-oat01-example"
	// A normal OAuth record stamps is_setup_token:false explicitly, as the
	// RequestAnthropicToken handler does.
	normal := &coreauth.Auth{
		Provider: "claude",
		Metadata: map[string]any{
			"email":          "user@example.com",
			"is_setup_token": false,
		},
	}
	// The prior file on disk was a setup token.
	existing := map[string]any{
		"access_token":   token,
		"is_setup_token": true,
		"auth_kind":      "setup_token",
	}
	coreauth.MergeExistingAuthMetadata(normal, existing)

	if v, _ := normal.Metadata["is_setup_token"].(bool); v {
		t.Fatalf("normal OAuth must not inherit is_setup_token:true, got %v", normal.Metadata["is_setup_token"])
	}
	normal.Metadata["access_token"] = token
	if coreauth.IsClaudeSetupTokenCredential(normal, token) {
		t.Fatal("normal OAuth credential must not be classified as a setup token after overwriting a setup-token file")
	}
}
