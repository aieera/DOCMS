package provisioner

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/billing/internal/model"
)

// The residency guard fires BEFORE any DB call — confirmed by passing
// nil deps + zero-value repo. If the check is bypassed and Provision
// proceeds, the nil panics; if the check fires, we get the expected
// "residency mismatch" error and never touch the deps.

func TestProvision_CrossRegion_Refused(t *testing.T) {
	p := NewWithRegion("uae-central", nil, nil, nil, zerolog.Nop())
	_, err := p.Provision(context.Background(), model.ProvisionRequest{
		OrgName: "Acme US",
		Plan:    "standard",
		Region:  "us-east-1",
	})
	if err == nil {
		t.Fatal("expected residency mismatch error; got nil")
	}
	if !strings.Contains(err.Error(), "residency mismatch") {
		t.Fatalf("error must mention residency mismatch; got %v", err)
	}
}

func TestProvision_EmptyRegion_Refused(t *testing.T) {
	// Stripe metadata that doesn't carry data_residency_region must
	// not silently default to the cluster region — the checkout flow
	// has to make the choice explicit.
	p := NewWithRegion("uae-central", nil, nil, nil, zerolog.Nop())
	_, err := p.Provision(context.Background(), model.ProvisionRequest{
		OrgName: "Acme",
		Plan:    "standard",
		Region:  "",
	})
	if err == nil {
		t.Fatal("expected required-region error; got nil")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Fatalf("error must mention required; got %v", err)
	}
}

func TestProvision_DeprecatedConstructor_SkipsRegionCheck(t *testing.T) {
	// The deprecated New() constructor leaves clusterRegion empty.
	// We deliberately preserve the legacy behaviour so test harness
	// + dev seeders that don't care about residency don't grow a
	// new mock just for this. Once the harness is migrated this
	// case can be removed.
	p := New(nil, nil, nil, zerolog.Nop())
	if p.clusterRegion != "" {
		t.Fatalf("deprecated constructor must leave clusterRegion empty; got %q", p.clusterRegion)
	}
	// We don't run Provision() here — without deps the nil-pool
	// access would panic. The contract we're asserting is purely
	// that the residency branch is skipped, which the struct field
	// alone proves.
}
