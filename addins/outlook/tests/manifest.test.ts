// W-6: the Outlook add-in only reads the selected message (subject,
// sender, body, attachments via getAttachmentContentAsync) — ReadItem is
// enough. It requested ReadWriteMailbox, the broadest mailbox permission
// (full EWS access to the whole mailbox).
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const xml = readFileSync(resolve(__dirname, '../manifest.xml'), 'utf8')
// Ignore commented-out XML.
const live = xml.replace(/<!--[\s\S]*?-->/g, '')

describe('Outlook manifest permissions (W-6)', () => {
  it('requests ReadItem only', () => {
    const perms = [...live.matchAll(/<Permissions>\s*([^<]+?)\s*<\/Permissions>/g)].map((m) => m[1])
    expect(perms).toEqual(['ReadItem'])
    expect(live).not.toMatch(/ReadWriteMailbox|ReadWriteItem/)
  })

  it('does not use APIs that need more than ReadItem', () => {
    const src = ['src/taskpane/TaskPane.tsx', 'src/commands/commands.ts', 'src/api.ts']
      .map((f) => readFileSync(resolve(__dirname, '..', f), 'utf8'))
      .join('\n')
    expect(src).not.toMatch(/makeEwsRequestAsync|getCallbackTokenAsync|saveAsync|addFileAttachment|body\.setAsync/)
  })
})
