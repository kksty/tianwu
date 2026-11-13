/** Decode the native protobuf bytes field. Render text only; binary stays hex. */
export function bodyPreview(base64 = '', limit = 65536): {text: string; size: number; truncated: boolean; binary: boolean} {
  const bytes = Uint8Array.from(atob(base64), c => c.charCodeAt(0))
  const prefix = bytes.subarray(0, limit)
  try {
    // Streaming permits a UTF-8 character to straddle the preview boundary.
    const text = new TextDecoder('utf-8', {fatal:true}).decode(prefix, {stream:bytes.length > limit})
    if (text.includes('\u0000')) throw new Error('binary')
    return {text, size:bytes.length, truncated:bytes.length > limit, binary:false}
  } catch {
    return {text:Array.from(prefix, b => b.toString(16).padStart(2, '0')).join(' '), size:bytes.length, truncated:bytes.length > limit, binary:true}
  }
}
