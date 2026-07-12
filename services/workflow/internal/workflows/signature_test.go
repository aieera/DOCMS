package workflows

// SignatureWorkflow tests (ADR 0025 Wave 9) — the Temporal orchestration half
// of the DoD "2-signer ceremony completes in the test env with valid LTV
// output". The seal activity is stubbed here to the endpoint's contract
// (returns the sealed version + PAdES-B-LT level); the REAL seal + LTV
// validation is proven in the signature service
// (TestSealCeremony_TwoSigners_ProducesLTVValidatedByVerifier). Uses
// Temporal's in-memory TestWorkflowEnvironment — no external cluster.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

type capturedEvent struct {
	eventType string
	data      map[string]string
}

// sigStub satisfies the activity method set the SignatureWorkflow calls.
// SealSignatureCeremony returns a fixed LTV-sealed result; PublishEvent +
// seal calls are recorded for assertions.
type sigStub struct {
	mu        sync.Mutex
	events    []capturedEvent
	sealCalls int
}

func (s *sigStub) CreateTask(_ context.Context, _, _, _, _, _ string) error  { return nil }
func (s *sigStub) NotifyAssignee(_ context.Context, _, _, _, _ string) error { return nil }
func (s *sigStub) CompleteTask(_ context.Context, _, _ string, _ int, _, _ string) error {
	return nil
}
func (s *sigStub) SealSignatureCeremony(_ context.Context, _, _, _, _, _ string) (*SealCeremonyResult, error) {
	s.mu.Lock()
	s.sealCalls++
	s.mu.Unlock()
	return &SealCeremonyResult{NewVersionID: "v-sealed", Level: "PAdES-B-LT", Fingerprint: "fp3"}, nil
}
func (s *sigStub) PublishEvent(_ context.Context, _, eventType string, data map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, capturedEvent{eventType: eventType, data: data})
	return nil
}

func (s *sigStub) eventOf(t *testing.T, eventType string) map[string]string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.events {
		if s.events[i].eventType == eventType {
			return s.events[i].data
		}
	}
	return nil
}

func TestSignatureWorkflow_TwoSignerCeremony_SealsAndCompletes(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	stub := &sigStub{}
	env.RegisterActivity(stub)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignatureDecisionSignal, SignatureSignal{SignerID: "alice", Action: "sign"})
	}, 1*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignatureDecisionSignal, SignatureSignal{SignerID: "bob", Action: "sign"})
	}, 2*time.Millisecond)

	env.ExecuteWorkflow(SignatureWorkflow, SignatureInput{
		TenantID: "t", InstanceID: "i", RequestID: "req-1",
		DocumentID: "doc-1", VersionID: "v-1", InitiatedBy: "u-1",
		Signers: []string{"alice", "bob"},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var out SignatureOutcome
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "completed", out.Status)
	require.ElementsMatch(t, []string{"alice", "bob"}, out.Signed)
	// The workflow OWNS the seal: the sealed version + LTV level are set.
	require.Equal(t, "v-sealed", out.SealedVersionID, "workflow must produce the sealed version")
	require.Equal(t, "PAdES-B-LT", out.Level, "sealed output must be LTV level")
	require.Equal(t, 1, stub.sealCalls, "seal must run exactly once (after all signers)")

	// The completed event carries the SEALED version + level + request_id so
	// downstream consumers (and the fallback sealer's idempotency) key off it.
	completed := stub.eventOf(t, "dms.signature.completed.v1")
	require.NotNil(t, completed, "must emit dms.signature.completed.v1")
	require.Equal(t, "v-sealed", completed["version_id"])
	require.Equal(t, "req-1", completed["request_id"])
	require.Equal(t, "PAdES-B-LT", completed["level"])
}

func TestSignatureWorkflow_SequentialOrder_SealsAfterAllSign(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	stub := &sigStub{}
	env.RegisterActivity(stub)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignatureDecisionSignal, SignatureSignal{SignerID: "alice", Action: "sign"})
	}, 1*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignatureDecisionSignal, SignatureSignal{SignerID: "bob", Action: "sign"})
	}, 2*time.Millisecond)

	env.ExecuteWorkflow(SignatureWorkflow, SignatureInput{
		TenantID: "t", InstanceID: "i", RequestID: "req-2",
		DocumentID: "doc-2", VersionID: "v-2", InitiatedBy: "u",
		Signers:         []string{"alice", "bob"},
		SequentialOrder: true,
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var out SignatureOutcome
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "completed", out.Status)
	require.Equal(t, []string{"alice", "bob"}, out.Signed, "sequential ceremony signs in list order")
	require.Equal(t, "v-sealed", out.SealedVersionID)
	require.Equal(t, 1, stub.sealCalls)
}

func TestSignatureWorkflow_Decline_ShortCircuitsWithoutSeal(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	stub := &sigStub{}
	env.RegisterActivity(stub)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignatureDecisionSignal, SignatureSignal{SignerID: "alice", Action: "decline", Comment: "no"})
	}, 1*time.Millisecond)

	env.ExecuteWorkflow(SignatureWorkflow, SignatureInput{
		TenantID: "t", InstanceID: "i", RequestID: "req-3",
		DocumentID: "doc-3", VersionID: "v-3",
		Signers: []string{"alice", "bob"},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var out SignatureOutcome
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "declined", out.Status)
	require.Equal(t, []string{"alice"}, out.Declined)
	require.Zero(t, stub.sealCalls, "a declined ceremony must NOT seal")
	require.Nil(t, stub.eventOf(t, "dms.signature.completed.v1"), "a decline must not emit completed")
	require.NotNil(t, stub.eventOf(t, "dms.signature.declined.v1"), "a decline must emit declined")
}
