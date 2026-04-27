// Region catalog — keep in sync with services/document/migrations/000014's
// supported_regions seed and pkg/regionenforcer/boundaries.go. Three
// places that must agree; CI's TestBoundariesMatchMigration covers
// SQL↔Go, this file is the third leg.

export type RegionBoundary = 'EU' | 'US' | 'MENA' | 'APAC' | 'OTHER'

export interface Region {
  code: string
  displayName: string
  boundary: RegionBoundary
}

export const REGIONS: Region[] = [
  { code: 'us-east-1',      displayName: 'US East (N. Virginia)',  boundary: 'US' },
  { code: 'us-west-2',      displayName: 'US West (Oregon)',       boundary: 'US' },
  { code: 'eu-west-1',      displayName: 'EU West (Ireland)',      boundary: 'EU' },
  { code: 'eu-central-1',   displayName: 'EU Central (Frankfurt)', boundary: 'EU' },
  { code: 'me-south-1',     displayName: 'Middle East (Bahrain)',  boundary: 'MENA' },
  { code: 'ap-southeast-1', displayName: 'APAC (Singapore)',       boundary: 'APAC' },
  { code: 'ap-northeast-1', displayName: 'APAC (Tokyo)',           boundary: 'APAC' },
  { code: 'custom',         displayName: 'Custom / on-prem',       boundary: 'OTHER' },
]

export function regionByCode(code: string): Region | undefined {
  return REGIONS.find((r) => r.code === code)
}
