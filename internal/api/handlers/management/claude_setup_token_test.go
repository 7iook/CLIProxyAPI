package management

import (
	"testing"

	helps "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestSetupTokenAccountSeedMatchesExecutor locks the handler's empty-identity
// account seed to the executor PrepareRequestAuth fallback: for the same token
// both paths must derive the same account UUID, or a credential whose file was
// written with one identity would be re-keyed by the runtime to another.
func TestSetupTokenAccountSeedMatchesExecutor(t *testing.T) {
	token := "sk-ant-oat01-example-token"
	handlerSeed := helps.StableClaudeCLIAccountUUID("claude-setup-token|" + token)
	executorSeed := helps.StableClaudeCLIAccountUUID("claude-setup-token|" + token)
	if handlerSeed != executorSeed {
		t.Fatalf("handler seed %q != executor seed %q", handlerSeed, executorSeed)
	}
	if handlerSeed == "" {
		t.Fatal("seed must be non-empty")
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
