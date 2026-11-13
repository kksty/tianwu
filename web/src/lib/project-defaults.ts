// Built-in project constraints and the project-name rule the console shows.
//
// Creating an engagement should need one paragraph: where we are starting and
// what the operator wants. The standing constraints (read-only first, stay in
// scope, protect target data, stop on unclear ownership) are a document the
// operator can edit or cancel. Evidence and handoff requirements belong to
// the role prompts and continue to apply when this document is cancelled,
// not something the program parses. It becomes the project `description`,
// which the brief/decision paths carry verbatim.
//
// The text is canonical copy: tests pin it so a casual edit cannot quietly
// weaken a standing constraint.

export const DEFAULT_PROJECT_CONSTRAINTS =
  '1. 只测试项目简报及后续明确授权的资产，并遵守约定的测试时间；不自行扩大范围，不扫描无关网段/域名。\n' +
  '2. 默认仅做只读验证；未经明确授权，不新增、修改或删除目标系统的业务数据、账号、权限、配置和文件。\n' +
  '3. 不执行影响可用性、不可逆或触发真实业务副作用的操作；确需执行时，先说明影响并等待单独授权。\n' +
  '4. 对范围外、归属不明或可能影响他人的资产，暂停涉及该资产的测试，由操作员确认后再继续。'

/** The description a creation request should carry: a cancelled document
 *  (switch off) is sent as empty, never as stale text. */
export function projectDescriptionDraft(enabled: boolean, text: string): string {
  return enabled ? text.trim() : ''
}

/**
 * Local preview of the server's name derivation (cmd/tw/hub.go
 * projectNameFromBrief): the first non-empty line, trimmed, at most 40 runes,
 * with an ellipsis when clipped; an empty brief falls back to 未命名项目. The
 * server stays authoritative; this exists so "leave the name empty" is not a
 * mystery in the dialog.
 */
export function projectNameFromBrief(brief: string): string {
  for (const line of brief.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed) continue
    const runes = Array.from(trimmed)
    return runes.length > 40 ? `${runes.slice(0, 40).join('')}…` : trimmed
  }
  return '未命名项目'
}
