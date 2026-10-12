package ingestroute

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	temporalclient "go.temporal.io/sdk/client"
)

type fakeMsg struct{ acked, naked, termed int }

func (m *fakeMsg) Ack(...nats.AckOpt) error  { m.acked++; return nil }
func (m *fakeMsg) Nak(...nats.AckOpt) error  { m.naked++; return nil }
func (m *fakeMsg) Term(...nats.AckOpt) error { m.termed++; return nil }

type fakeStarter struct {
	opts []temporalclient.StartWorkflowOptions
	ins  []IngestInput
}

func (f *fakeStarter) ExecuteWorkflow(_ context.Context, o temporalclient.StartWorkflowOptions, _ interface{}, args ...interface{}) (temporalclient.WorkflowRun, error) {
	f.opts = append(f.opts, o)
	f.ins = append(f.ins, args[0].(IngestInput))
	return nil, nil
}

func processedEvent(envTenant, dataTenant, item string) []byte {
	data, _ := json.Marshal(map[string]string{"ingestion_item_id": item, "tenant_id": dataTenant})
	raw, _ := json.Marshal(map[string]any{"specversion": "1.0", "id": uuid.NewString(), "tenantid": envTenant, "data": json.RawMessage(data)})
	return raw
}

// D-24: the consumer started a routing workflow for whatever tenant string
// the event carried, acked malformed events as if handled, and started with
// REJECT_DUPLICATE so an item whose workflow failed could never be routed
// again. A malformed or inconsistent tenant / item id is now terminated
// (JetStream Term: no redelivery, surfaced as a terminated advisory), the
// tenant handed to the workflow is the parsed UUID, and the id-reuse policy
// lets a failed run be retried while a running or completed one stays
// deduplicated.
func TestIngestRouteConsumer_MalformedTenantTerminated(t *testing.T) {
	tenant, item := uuid.New(), uuid.New()
	for name, raw := range map[string][]byte{
		"bad envelope":         []byte("{not json"),
		"non-uuid tenant":      processedEvent("", "tenant-a", item.String()),
		"nil tenant":           processedEvent("", uuid.Nil.String(), item.String()),
		"no tenant":            processedEvent("", "", item.String()),
		"envelope/data differ": processedEvent(uuid.NewString(), tenant.String(), item.String()),
		"non-uuid item":        processedEvent(tenant.String(), tenant.String(), "item-1"),
		"missing item":         processedEvent(tenant.String(), tenant.String(), ""),
	} {
		st := &fakeStarter{}
		c := &Consumer{tc: st, log: zerolog.Nop()}
		m := &fakeMsg{}
		c.process(m, raw)
		require.Equal(t, 1, m.termed, name)
		require.Zero(t, m.acked+m.naked, name)
		require.Empty(t, st.opts, "%s: no workflow for a malformed event", name)
	}

	st := &fakeStarter{}
	c := &Consumer{tc: st, log: zerolog.Nop()}
	m := &fakeMsg{}
	c.process(m, processedEvent(tenant.String(), " "+tenant.String()+" ", item.String()))
	require.Equal(t, 1, m.acked)
	require.Len(t, st.opts, 1)
	require.Equal(t, "ingest-"+item.String(), st.opts[0].ID)
	require.Equal(t, enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY, st.opts[0].WorkflowIDReusePolicy)
	require.Equal(t, IngestInput{TenantID: tenant.String(), ItemID: item.String()}, st.ins[0])
}
