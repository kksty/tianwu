// The evidence preview is one reading surface shared by the evidence page, the
// graph inspector and the vulnerability library. These tests pin the source
// mapping (a ledger row vs a library path) and the two decisions the sheet makes
// from response metadata: what viewer to use, and whether the read was partial.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  contentRangeTotal,
  evidencePreviewFile,
  previewFormat,
  previewTruncated,
  vulnEvidencePreviewFile,
} from '../src/lib/evidence-preview.ts'

test('a vulnerability library path becomes an inline preview and a separate download', () => {
  const file = vulnEvidencePreviewFile('evidence/acme/in_x/a capture.html')
  assert.equal(file.key, 'evidence/acme/in_x/a capture.html')
  assert.equal(file.name, 'a capture.html')
  assert.equal(file.uri, 'evidence/acme/in_x/a capture.html')
  // The preview URL stays inline; saving is the explicit ?download=1 request.
  assert.equal(file.contentURL, '/api/vulns/evidence/evidence/acme/in_x/a%20capture.html')
  assert.equal(file.downloadURL, '/api/vulns/evidence/evidence/acme/in_x/a%20capture.html?download=1')
  // The library has no per-file ledger row, so nothing is invented here.
  assert.equal(file.size, undefined)
  assert.equal(file.mime, undefined)
})

test('a project evidence row keeps its ledger metadata and project-scoped URLs', () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { location: { pathname: '/p/acme/' } },
  })
  try {
    const file = evidencePreviewFile({
      id: 'e_1',
      run_id: 'r_1',
      intent_id: 'in_1',
      name: 'page.html',
      mime: 'application/octet-stream',
      size: 42,
      uri: 'in_1/page.html',
      created_at: '2026-11-09T00:00:00Z',
    })
    assert.equal(file.key, 'e_1')
    assert.equal(file.name, 'page.html')
    assert.equal(file.mime, 'application/octet-stream')
    assert.equal(file.size, 42)
    assert.equal(file.contentURL, '/p/acme/api/evidence/e_1')
    assert.equal(file.downloadURL, '/p/acme/api/evidence/e_1?download=1')
  } finally {
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('preview format follows the recorded mime first, then the file name', () => {
  assert.equal(previewFormat({ mime: 'image/png', name: 'shot.bin', uri: '' }), 'image')
  assert.equal(previewFormat({ mime: 'application/pdf', name: 'doc.bin', uri: '' }), 'pdf')
  // SVG is text on purpose: rendering it as a document would execute script.
  assert.equal(previewFormat({ mime: 'image/svg+xml', name: 'icon.svg', uri: '' }), 'other')
  // Library copies have only a name; the extension still selects the viewer.
  assert.equal(previewFormat({ name: 'shot.PNG', uri: '' }), 'image')
  assert.equal(previewFormat({ name: 'report.pdf', uri: '' }), 'pdf')
  assert.equal(previewFormat({ name: 'capture.html', uri: '' }), 'other')
  assert.equal(previewFormat({ name: 'blob', uri: '' }), 'other')
})

test('content range total is read from the server measurement only', () => {
  assert.equal(contentRangeTotal('bytes 0-1048575/2048'), 2048)
  assert.equal(contentRangeTotal('BYTES 0-9/12345678'), 12345678)
  assert.equal(contentRangeTotal('bytes 0-9/*'), undefined)
  assert.equal(contentRangeTotal(''), undefined)
  assert.equal(contentRangeTotal(null), undefined)
  assert.equal(contentRangeTotal('not a range'), undefined)
})

test('a preview is truncated only when a source proves it', () => {
  // The server said how large the file is.
  assert.equal(previewTruncated(undefined, 1_048_576, 2_000_000), true)
  assert.equal(previewTruncated(undefined, 1_048_576, 1_048_576), false)
  // No Content-Range, but the ledger recorded the size.
  assert.equal(previewTruncated(2_000_000, 1_048_576, undefined), true)
  assert.equal(previewTruncated(1_048_576, 1_048_576, undefined), false)
  // A library copy of exactly the preview window is not called truncated: no
  // source reported its real size, so the sheet must not guess.
  assert.equal(previewTruncated(undefined, 1_048_576, undefined), false)
})
