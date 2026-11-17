// Generates hashvectors.json with the exact algorithm floccus v5.13.0 uses
// (src/lib/Tree.ts Bookmark#hash / Folder#hash, src/lib/Crypto.ts sha256):
// sha256 over JS JSON.stringify output, lowercase hex. Run: node genhashvectors.mjs
//
// Special characters are built with String.fromCharCode so no editor, shell or
// tool can silently rewrite them, and the fixture is written with every
// non-ASCII character as a JSON \uXXXX escape. A literal backslash followed by
// "u2028" is plain ASCII text and must stay distinct from the real U+2028.
import { webcrypto } from 'node:crypto'
import { writeFileSync } from 'node:fs'

const sha256 = async (s) =>
  Array.from(new Uint8Array(await webcrypto.subtle.digest('SHA-256', new TextEncoder().encode(s))))
    .map((b) => ('00' + b.toString(16)).slice(-2)).join('')

const bookmark = (title, url) => ({ type: 'bookmark', title, url })
const folder = (title, ...children) => ({ type: 'folder', title, children })

async function hash(node, isRoot = false) {
  if (node.type === 'bookmark') return sha256(JSON.stringify({ title: node.title, url: node.url }))
  const childHashes = []
  for (const c of node.children) childHashes.push(await hash(c))
  // The root (-1) has no title, so JSON.stringify drops the key.
  const o = isRoot ? { title: undefined, children: childHashes } : { title: node.title, children: childHashes }
  return sha256(JSON.stringify(o))
}
async function annotate(node, isRoot = false) {
  node.hash = await hash(node, isRoot)
  for (const c of node.children || []) await annotate(c)
  return node
}

const ch = (n) => String.fromCharCode(n)
const LS = ch(0x2028)
const PS = ch(0x2029)
const BS = ch(92) // one literal backslash
const U2028_TEXT = BS + 'u2028' // 6 ASCII characters, not a line separator
const U2029_TEXT = BS + 'u2029'
const U003C_TEXT = BS + 'u003c'

const root = folder(undefined,
  folder('Bookmarks Bar',
    bookmark('plain', 'https://example.com/a?b=c&d=e'),
    bookmark('quotes "double" \'single\'', 'https://example.com/q'),
    bookmark('back' + BS + 'slash ' + BS + BS + ' pair', 'https://example.com/back' + BS + 'slash'),
    bookmark('html <b>&amp; "x"</b>', 'https://example.com/?a=<1>&b=2'),
    bookmark('raw line sep [' + LS + '] para sep [' + PS + ']', 'https://example.com/ls' + LS + 'ps' + PS),
    bookmark('literal text ' + U2028_TEXT + ' and ' + U2029_TEXT + ' and ' + U003C_TEXT, 'https://example.com/lit' + U2028_TEXT),
    bookmark('unicode ' + ch(0xe9) + ' ' + ch(0x65e5) + ch(0x672c) + ' ' + ch(0xd83d) + ch(0xde00) + ' ' + ch(0x627) + ch(0x644), 'https://example.com/' + ch(0xfc) + '/' + ch(0x65e5)),
    bookmark('controls ' + ch(1) + ' \t \n \r \b \f ' + ch(0x1f) + ' del' + ch(0x7f), 'https://example.com/ctl'),
    bookmark('', 'https://example.com/empty-title'),
    folder('Nested <&> "quote" ' + U2028_TEXT + ' ' + LS,
      folder('deep', folder('deeper', bookmark('leaf', 'https://example.com/leaf'))),
      bookmark('slashes', 'https://example.com/a/b/c//d'),
      folder('empty'),
    ),
  ),
  folder('Other Bookmarks'),
)
await annotate(root, true)

// Escape every UTF-16 code unit from 0x7f up as a JSON escape (what a reader
// of the fixture must decode back). ASCII backslashes are untouched, so literal
// text consisting of a backslash plus u2028 stays distinct from a real line separator.
const escapeNonAscii = (s) =>
  s.split("").map((c) => (c.charCodeAt(0) >= 0x7f ? BS + "u" + c.charCodeAt(0).toString(16).padStart(4, "0") : c)).join("")
writeFileSync(new URL("./hashvectors.json", import.meta.url), escapeNonAscii(JSON.stringify(root, null, 1)) + String.fromCharCode(10))
console.log("root hash", root.hash)
