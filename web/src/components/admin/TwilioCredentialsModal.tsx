import { PasswordInput } from '@/components/ui/PasswordInput'
import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'

import { getTwilioConfig, saveTwilioConfig, testTwilio } from '@/api/notif-providers'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogDescription, DialogFooter,
} from '@/components/ui/shadcn/dialog'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void | Promise<void>
}

export function TwilioCredentialsModal({ open, onOpenChange, onSaved }: Props) {
  const qc = useQueryClient()
  const existingQ = useQuery({
    queryKey: ['twilio-config'],
    queryFn: getTwilioConfig,
    enabled: open,
  })

  const [accountSID, setAccountSID] = useState('')
  const [authToken, setAuthToken] = useState('')
  const [verifySID, setVerifySID] = useState('')
  const [testPhone, setTestPhone] = useState('')

  useEffect(() => {
    if (existingQ.data) {
      setAccountSID(existingQ.data.account_sid)
      setVerifySID(existingQ.data.verify_service_sid)
    }
  }, [existingQ.data])

  const hasExistingToken = !!existingQ.data?.has_auth_token

  const saveMut = useAppMutation({
    mutationFn: () => saveTwilioConfig({
      account_sid: accountSID.trim(),
      auth_token: authToken,
      verify_service_sid: verifySID.trim(),
    }),
    onSuccess: async () => {
      toast.success('Twilio credentials saved')
      qc.invalidateQueries({ queryKey: ['twilio-config'] })
      await onSaved()
    },
    onError: (e: Error) => toast.error(e.message || 'Save failed'),
  })

  const testMut = useAppMutation({
    mutationFn: () => testTwilio(testPhone),
    onSuccess: () => toast.success(`Test SMS sent to ${testPhone}`),
    onError: (e: Error) => toast.error(e.message || 'Test failed'),
  })

  const canSubmit = accountSID.trim() !== ''
    && verifySID.trim() !== ''
    && (hasExistingToken || authToken !== '')
    && !saveMut.isPending

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Twilio (SMS MFA)</DialogTitle>
          <DialogDescription>
            Create a Verify Service in Twilio Console → Verify → Services, then paste the credentials here.
            {hasExistingToken && ' Leave Auth Token blank to keep the existing one.'}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div>
            <label className="mb-1 block text-sm font-medium">Account SID</label>
            <Input
              value={accountSID}
              onChange={(e) => setAccountSID(e.target.value)}
              placeholder="ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
              autoComplete="off"
              spellCheck={false}
            />
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium">
              Auth Token
              {hasExistingToken && <span className="ms-2 text-xs text-muted-foreground">(stored — paste a new one to replace)</span>}
            </label>
            <PasswordInput
              value={authToken}
              onChange={(e) => setAuthToken(e.target.value)}
              placeholder={hasExistingToken ? '••••••••' : 'paste once — never re-shown'}
              autoComplete="new-password"
            />
          </div>
          <div>
            <label className="mb-1 block text-sm font-medium">Verify Service SID</label>
            <Input
              value={verifySID}
              onChange={(e) => setVerifySID(e.target.value)}
              placeholder="VAxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
              autoComplete="off"
              spellCheck={false}
            />
          </div>

          {hasExistingToken && (
            <div className="rounded-md border border-border bg-muted/30 p-3">
              <p className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">Send test SMS</p>
              <div className="flex gap-2">
                <Input
                  value={testPhone}
                  onChange={(e) => setTestPhone(e.target.value)}
                  placeholder="+14155552671"
                />
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => testMut.mutate()}
                  disabled={!testPhone.startsWith('+') || testMut.isPending}
                >
                  {testMut.isPending ? 'Sending…' : 'Send test'}
                </Button>
              </div>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={() => saveMut.mutate()} disabled={!canSubmit}>
            {saveMut.isPending ? 'Saving…' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
