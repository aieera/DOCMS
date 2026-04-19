// Package model holds domain types for the billing service.
package model

import "time"

// Plan defines a subscription tier.
type Plan struct {
	ID                 string `json:"id"` // standard | enterprise | dedicated
	Name               string `json:"name"`
	StorageLimitGB     int64  `json:"storage_limit_gb"`
	MaxUsers           int    `json:"max_users"`
	PriceMonthly       int64  `json:"price_monthly_cents"`
	StripePriceID      string `json:"stripe_price_id,omitempty"`
	IncludedOCRPages   int    `json:"included_ocr_pages"`
	IncludedAITokens   int64  `json:"included_ai_tokens"`
}

// DefaultPlans are the built-in plan definitions.
var DefaultPlans = map[string]Plan{
	"standard": {
		ID: "standard", Name: "Standard", StorageLimitGB: 100,
		MaxUsers: 25, PriceMonthly: 9900, IncludedOCRPages: 5000, IncludedAITokens: 500_000,
	},
	"enterprise": {
		ID: "enterprise", Name: "Enterprise", StorageLimitGB: 1000,
		MaxUsers: 250, PriceMonthly: 49900, IncludedOCRPages: 50_000, IncludedAITokens: 5_000_000,
	},
	"dedicated": {
		ID: "dedicated", Name: "Dedicated", StorageLimitGB: 10000,
		MaxUsers: -1, PriceMonthly: 199900, IncludedOCRPages: -1, IncludedAITokens: -1,
	},
}

// Subscription tracks a tenant's active subscription.
type Subscription struct {
	TenantID          string     `json:"tenant_id"`
	PlanID            string     `json:"plan_id"`
	StripeCustomerID  string     `json:"stripe_customer_id,omitempty"`
	StripeSubID       string     `json:"stripe_subscription_id,omitempty"`
	Status            string     `json:"status"` // active | past_due | suspended | cancelled
	GracePeriodEnds   *time.Time `json:"grace_period_ends,omitempty"`
	CurrentPeriodStart time.Time `json:"current_period_start"`
	CurrentPeriodEnd   time.Time `json:"current_period_end"`
	CreatedAt         time.Time  `json:"created_at"`
}

// UsageRecord is one hourly metering snapshot.
type UsageRecord struct {
	TenantID    string    `json:"tenant_id"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	StorageGB   float64   `json:"storage_gb"`
	OCRPages    int64     `json:"ocr_pages"`
	APICalls    int64     `json:"api_calls"`
	ActiveUsers int       `json:"active_users"`
	AITokens    int64     `json:"ai_tokens"`
}

// FeatureFlags per-tenant feature toggles stored in organizations.settings.
type FeatureFlags struct {
	AIEnabled        bool `json:"ai_enabled"`
	AdvancedWorkflow bool `json:"advanced_workflow"`
	SSOEnabled       bool `json:"sso_enabled"`
	ESignatures      bool `json:"e_signatures"`
	CustomBranding   bool `json:"custom_branding"`
	APIAccess        bool `json:"api_access"`
	DataRooms        bool `json:"data_rooms"`
}

// DefaultFlagsByPlan returns default feature flags for a plan.
func DefaultFlagsByPlan(planID string) FeatureFlags {
	switch planID {
	case "enterprise":
		return FeatureFlags{AIEnabled: true, AdvancedWorkflow: true, SSOEnabled: true, ESignatures: true, CustomBranding: true, APIAccess: true, DataRooms: false}
	case "dedicated":
		return FeatureFlags{AIEnabled: true, AdvancedWorkflow: true, SSOEnabled: true, ESignatures: true, CustomBranding: true, APIAccess: true, DataRooms: true}
	default: // standard
		return FeatureFlags{AIEnabled: false, AdvancedWorkflow: false, SSOEnabled: false, ESignatures: false, CustomBranding: false, APIAccess: true, DataRooms: false}
	}
}

// ProvisionRequest is the input to tenant provisioning.
type ProvisionRequest struct {
	OrgName    string `json:"org_name"`
	AdminEmail string `json:"admin_email"`
	Plan       string `json:"plan"`
	Region     string `json:"region"`
}

// ProvisionResult is returned after successful provisioning.
type ProvisionResult struct {
	TenantID    string `json:"tenant_id"`
	AdminUserID string `json:"admin_user_id"`
	LoginURL    string `json:"login_url"`
}
