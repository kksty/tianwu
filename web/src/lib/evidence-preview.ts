// One previewable artifact, independent of which endpoint produced it. Project
// evidence rows are addressed by their ledger id; vulnerability-library copies
// by their library-relative path. The preview sheet decides what to render from
// this shape and never branches on where the file came from, so there is one
// reading surface instead of a second implementation per page.
//
// A value the source did not record stays absent: a library copy has no ledger
// row, so its mime and size are discovered from the response instead of being
// invented here.

import { api } from './api.ts'
import type { Evidence } from './types.ts'

/** One range request caps what a huge log can cost the DOM. */
export const EVIDENCE_PREVIEW_BYTES = 1024 * 1024

export type PreviewFormat = 'image' | 'pdf' | 'other'

export interface PreviewFile {
  /** Stable identity for the fetch effect; the content URL is what it names. */
  key: string
  name: string
  uri: string
  mime?: string
  size?: number
  createdAt?: string
  intentID?: string
  runID?: string
  /** Inline preview endpoint (never a download: the server answers inline). */
  contentURL: string
  /** Explicit save endpoint (the server answers with an attachment name). */
  downloadURL: string
}

/** A project evidence row: the ledger knows its id, mime and size. */
export function evidencePreviewFile(e: Evidence): PreviewFile {
  return {
    key: e.id,
    name: e.name || e.uri.split('/').pop() || e.id,
    uri: e.uri,
    mime: e.mime,
    size: e.size,
    createdAt: e.created_at,
    intentID: e.intent_id,
    runID: e.run_id,
    contentURL: api.evidenceURL(e.id),
    downloadURL: api.evidenceDownloadURL(e.id),
  }
}

/**
 * A vulnerability-library evidence copy. The copy has no per-file ledger row,
 * so the path is the identity and the name is the only metadata the path can
 * prove; the response supplies mime and total size when the preview fetches it.
 */
export function vulnEvidencePreviewFile(path: string): PreviewFile {
  return {
    key: path,
    name: path.split('/').pop() || path,
    uri: path,
    contentURL: api.vulnEvidenceURL(path),
    downloadURL: api.vulnEvidenceDownloadURL(path),
  }
}

const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'avif', 'ico'])

/**
 * The one decision the browser viewer needs: an element, a plugin, or raw text.
 * SVG is text on purpose: its source is what an operator wants to read, and
 * handing an SVG document to the browser would be an inline-script surface.
 * Image types come from the recorded mime when there is one, else the name —
 * library copies only have the name.
 */
export function previewFormat(file: Pick<PreviewFile, 'mime' | 'name' | 'uri'>): PreviewFormat {
  const mime = (file.mime ?? '').toLowerCase()
  if (mime.startsWith('image/') && mime !== 'image/svg+xml') return 'image'
  if (mime === 'application/pdf') return 'pdf'
  const name = file.name || file.uri
  const dot = name.lastIndexOf('.')
  const ext = dot >= 0 ? name.slice(dot + 1).toLowerCase() : ''
  if (IMAGE_EXTS.has(ext)) return 'image'
  if (ext === 'pdf') return 'pdf'
  return 'other'
}

/**
 * Total file size from a Content-Range response header, when the server
 * answered a bounded read: "bytes 0-1048575/12345678" → 12345678. An unknown
 * total ("*") and malformed headers stay undefined instead of guessing.
 */
export function contentRangeTotal(value: string | null | undefined): number | undefined {
  if (!value) return undefined
  const match = /^bytes\s+\d+-\d+\/(\d+)$/i.exec(value.trim())
  if (!match) return undefined
  const total = Number(match[1])
  return Number.isFinite(total) && total >= 0 ? total : undefined
}

/**
 * Whether the preview received less than the whole file. Only proven values
 * count: a file that merely happens to be exactly the preview window is not
 * reported as truncated, because no source told us its real size.
 */
export function previewTruncated(
  size: number | undefined,
  received: number,
  total: number | undefined,
): boolean {
  if (total !== undefined) return total > received
  if (size !== undefined) return size > received
  return false
}
