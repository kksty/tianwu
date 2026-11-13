import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const files = [...new Set(execFileSync('git', ['ls-files', '-z', '--cached', '--others', '--exclude-standard', '--', '*.md'], {cwd: root, encoding: 'utf8'}).split('\0').filter(Boolean))].sort()
const problems = []
const anchors = new Map()
let checked = 0
const references = new Map()

function documentText(file) {
  return readFileSync(file, 'utf8').replace(/^\s*(`{3,}|~{3,})[^\n]*\n[\s\S]*?^\s*\1\s*$/gm, '')
}

function documentAnchors(file) {
  if (anchors.has(file)) return anchors.get(file)
  const text = documentText(file)
  const ids = new Set([...text.matchAll(/\b(?:id|name)=["']([^"']+)["']/g)].map(match => match[1]))
  const duplicates = new Map()
  for (const match of text.matchAll(/^ {0,3}#{1,6}\s+(.+?)\s*#*\s*$/gm)) {
    const slug = match[1].replace(/<[^>]*>/g, '').replace(/\[([^\]]*)\]\([^)]*\)/g, '$1').toLowerCase()
      .replace(/[^\p{L}\p{N}\p{M}_\-\s]/gu, '').trim().replace(/\s/g, '-')
    const count = duplicates.get(slug) ?? 0
    duplicates.set(slug, count + 1)
    ids.add(count ? `${slug}-${count}` : slug)
  }
  anchors.set(file, ids)
  return ids
}

for (const file of files) {
  const source = path.join(root, file)
  if (!existsSync(source)) continue
  const text = documentText(source).replace(/(`+)([\s\S]*?)\1/g, '')
  references.set(source, new Set())
  const links = [
    ...[...text.matchAll(/!?\[[^\]\n]*\]\(\s*(<[^>]+>|[^\s)]+)(?:\s+[^)]*)?\)/g)].map(match => match[1].replace(/^<|>$/g, '')),
    ...[...text.matchAll(/^ {0,3}\[[^\]]+\]:\s*(\S+)/gm)].map(match => match[1].replace(/^<|>$/g, '')),
  ]
  for (const href of links) {
    // Network links, app routes and runtime resource URIs are not repository
    // files. This command checks local documentation without network access.
    if (/^[a-z][a-z0-9+.-]*:|^\/\//i.test(href) || /^\/(?:api|p|g|static)(?:\/|\?|$)/.test(href)) continue
    let destination, fragment
    try {
      const hash = href.indexOf('#')
      destination = decodeURIComponent((hash < 0 ? href : href.slice(0, hash)).split('?')[0])
      fragment = hash < 0 ? '' : decodeURIComponent(href.slice(hash + 1))
    } catch {
      problems.push(`${file}: invalid URL encoding: ${href}`)
      continue
    }
    const target = destination ? path.resolve(destination.startsWith('/') ? root : path.dirname(source), destination.replace(/^\//, '')) : source
    checked++
    if (target !== root && !target.startsWith(root + path.sep)) {
      problems.push(`${file}: target leaves repository: ${href}`)
      continue
    }
    if (!existsSync(target)) {
      problems.push(`${file}: missing file: ${href}`)
      continue
    }
    if (/\.md$/i.test(target)) references.get(source).add(target)
    if (fragment && statSync(target).isFile() && /\.md$/i.test(target) && !documentAnchors(target).has(fragment)) {
      problems.push(`${file}: missing anchor: ${href}`)
    }
  }
}

// Every maintained document must be discoverable through the README/index
// graph, including history and embedded content-source instructions.
const reachable = new Set()
const pending = [path.join(root, 'README.md')]
while (pending.length) {
  const file = pending.pop()
  if (reachable.has(file)) continue
  reachable.add(file)
  pending.push(...(references.get(file) ?? []))
}
for (const file of files) {
  if (existsSync(path.join(root, file)) && !reachable.has(path.join(root, file))) problems.push(`${file}: not reachable from README.md`)
}

for (const problem of problems) process.stderr.write(`${problem}\n`)
process.stdout.write(`Checked ${files.length} Markdown documents and ${checked} local links; ${problems.length} problems.\n`)
if (problems.length) process.exitCode = 1
