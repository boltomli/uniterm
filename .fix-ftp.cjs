const fs = require('fs')
const p = 'backend/session/ftp_session_test.go'
let s = fs.readFileSync(p, 'utf8')
const lines = s.split('\n')
const idx = lines.findIndex(l => l.includes('FTPSession.Copy must stream through io.Copy'))
console.log('idx', idx, JSON.stringify(lines.slice(idx, idx + 6)))
// drop the orphaned tail: "}" then "\t\tt.Fatal(...)" and the two braces that follow
let i = idx + 1
if (lines[i].trim() === '}') i++
console.log('after close at', i, JSON.stringify(lines.slice(i, i + 4)))
if (lines[i].includes('Read blocked after cancellation')) {
  lines.splice(i, 3) // t.Fatal line + two closing braces
}
fs.writeFileSync(p, lines.join('\n'))
console.log('done')
