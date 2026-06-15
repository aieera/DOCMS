import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'

// Customer Files — the ERP↔SeDoc file explorer, surfaced as a top-level section
// (peer of Workspaces). It's a SEPARATE product: its own Files BFF holds the
// service API key and authorizes per-customer via the ERP. Creating a customer in
// the ERP auto-provisions that customer's folder tree (Quotes/POs/SOs/DOs/
// Invoices/Attachments) and committed documents file into it automatically.
//
// Embedded via iframe from the integration web app on :5180 of the same host
// (works via localhost or a LAN IP). Auth inside the panel is the ERP user's
// (the panel's identity control stands in until the ERP SSO proxy is wired) —
// distinct from this SeDoc session, which is why it's embedded, not mounted inline.
function customerFilesURL(): string {
  return `${window.location.protocol}//${window.location.hostname}:5180`
}

function CustomerFilesPage() {
  const url = customerFilesURL()
  return (
    <div className="space-y-4">
      <PageHeader
        title="Customer Files"
        description="ERP-keyed document explorer. Each customer's folder tree is created automatically when the customer is added in the ERP, and committed documents file into it."
      />
      <div className="overflow-hidden rounded-lg border border-border bg-muted/20">
        <iframe title="Customer Files" src={url} className="h-[80vh] w-full border-0" referrerPolicy="no-referrer" />
      </div>
      <p className="text-xs text-muted-foreground">
        Served by the ERP integration app at <span className="font-mono">{url}</span>. Set the ERP
        user in the panel’s identity control until the ERP SSO proxy is wired.
      </p>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/customer-files')({
  component: CustomerFilesPage,
})
