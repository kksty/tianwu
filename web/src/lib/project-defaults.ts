// Built-in project constraints the console proposes at creation time.
//
// Creating an engagement needs an explicit display name plus the operator's
// start and end: 起点 becomes the project brief, 终点 becomes the single endpoint
// goal. The directory id is a short random id decoupled from the name, so the
// console shows no derived-name preview. The standing constraints (read-only
// first, stay in scope, protect target data) are a document the operator can
// edit or cancel. Evidence and handoff requirements belong to the role prompts
// and continue to apply when this document is cancelled, not something the
// program parses. It becomes the project `description`, which the brief/decision
// paths carry verbatim.
//
// The text is canonical copy: tests pin it so a casual edit cannot quietly
// weaken a standing constraint.
//
// The former fourth clause (stop work on out-of-scope or unclear-ownership
// assets and ask the operator first) was removed by the operator on 2026-11-13.
// Its absence is intentional: do not "restore" it from git history as if it were
// a regression — the built-in document is exactly these three items.
//
// The operator also does not want the document to read as a blanket read-only
// rule: testing an upload endpoint (or cleaning up test data) needs controlled
// writes. So clause 2 is "default read-only + an explicit allowance for
// authorised, scoped writes/uploads on dedicated test data", while clause 3
// keeps the hard limits. "Never delete or modify real data" is the one rule the
// operator requires to stay absolute: no authorisation can relax it. The
// separately-authorisable actions are the other side effects (availability,
// irreversibility, non-data side effects) — not real-data changes.

export const DEFAULT_PROJECT_CONSTRAINTS =
  '1. 只测试项目简报及后续明确授权的资产，并遵守约定的测试时间；不自行扩大范围，不扫描无关网段/域名。\n' +
  '2. 默认只读；在简报或单独授权明确写明的范围内，可以做受控的写入、删改与上传（例如测试文件上传接口、创建并清理测试数据）：事先说明范围、内容与预期影响，只作用于授权资产和专用测试数据，不触碰真实数据，结束后按需清理或恢复。\n' +
  '3. 不删改真实数据（含真实业务数据、账号、权限、配置和文件）：这条不可放宽，即使有授权也不做真实数据的删除或修改；不执行影响可用性、不可逆或触发其他真实业务副作用的操作，确需执行时先说明影响并等待单独授权。'

/** The description a creation request should carry: a cancelled document
 *  (switch off) is sent as empty, never as stale text. */
export function projectDescriptionDraft(enabled: boolean, text: string): string {
  return enabled ? text.trim() : ''
}
