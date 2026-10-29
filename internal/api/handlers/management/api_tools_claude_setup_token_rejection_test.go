package management

import (
	"fmt"
	"net/http"
	"testing"
)

type fakeProbeRejection struct {
	status int
	body   string
}

func (e fakeProbeRejection) Error() string        { return e.body }
func (e fakeProbeRejection) UpstreamStatus() int  { return e.status }
func (e fakeProbeRejection) UpstreamBody() string { return e.body }

func TestClaudeSetupTokenProbeRejectionKeepsUpstreamAnswer(t *testing.T) {
	body := `{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."}}`
	resp, ok := claudeSetupTokenProbeRejection(fmt.Errorf("wrapped: %w", fakeProbeRejection{status: http.StatusForbidden, body: body}))
	if !ok || resp.StatusCode != http.StatusForbidden || resp.Body != body {
		t.Fatalf("got ok=%v resp=%+v", ok, resp)
	}
	if _, ok := claudeSetupTokenProbeRejection(fmt.Errorf("network down")); ok {
		t.Fatal("plain errors must fall back to the legacy path")
	}
}
