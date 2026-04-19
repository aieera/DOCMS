import { useState } from 'react'
import { Document, Page, pdfjs } from 'react-pdf'
import { Button } from '@/components/ui/Button'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { Spinner } from '@/components/ui/Spinner'

pdfjs.GlobalWorkerOptions.workerSrc = `//unpkg.com/pdfjs-dist@${pdfjs.version}/build/pdf.worker.min.mjs`

export function PDFViewer({ url }: { url: string; mimeType?: string }) {
  const [numPages, setNumPages] = useState(0)
  const [page, setPage] = useState(1)

  return (
    <div className="flex flex-col items-center">
      <Document file={url} onLoadSuccess={({ numPages: n }) => setNumPages(n)} loading={<Spinner />} error={<p className="text-sm text-red-500">Failed to load PDF</p>}>
        <Page pageNumber={page} width={800} renderTextLayer renderAnnotationLayer={false} />
      </Document>
      {numPages > 1 && (
        <div className="mt-3 flex items-center gap-2">
          <Button variant="ghost" size="sm" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}><ChevronLeft className="h-4 w-4" /></Button>
          <span className="text-sm">{page} / {numPages}</span>
          <Button variant="ghost" size="sm" disabled={page >= numPages} onClick={() => setPage((p) => p + 1)}><ChevronRight className="h-4 w-4" /></Button>
        </div>
      )}
    </div>
  )
}
