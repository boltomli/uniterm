const fs = require('fs')
const p = 'src/components/DBTreePanel.vue'
let s = fs.readFileSync(p, 'utf8')
const crlf = s.includes('\r\n')
if (crlf) s = s.replace(/\r\n/g, '\n')
function rep(a, b) {
  if (!s.includes(a)) throw new Error('missing ' + JSON.stringify(a.slice(0, 60)))
  s = s.replace(a, b)
}

rep(`let treeSeq = 0
`, `let treeSeq = 0
// Auto-opening a database also focuses the objects tab in the parent — that
// must happen only on the FIRST successful load of a session; a refresh must
// not steal the user back from the table/query they are looking at.
let autoOpened = false
`)

rep(`    databases.value = dbs.map((db: string) => fresh(db))
    expandedDbs.value = new Set()
  } catch (e) {`,
`    databases.value = dbs.map((db: string) => fresh(db))
    expandedDbs.value = new Set()
    // Auto-open the connection's default database on the FIRST load of this
    // session only (a refresh must not steal focus back). Oracle's dbName is
    // the service name and never matches, so its first schema is opened
    // instead; a single-database connection is opened too.
    let autoOpen = ''
    if (props.defaultDbName && dbs.includes(props.defaultDbName)) autoOpen = props.defaultDbName
    else if (dbs.length === 1) autoOpen = dbs[0]
    else if (dbs.length > 0 && props.dbType === 'oracle') autoOpen = dbs[0]
    if (autoOpen && !autoOpened) {
      autoOpened = true
      expandedDbs.value = new Set([autoOpen])
      selectedDb.value = autoOpen
      selectedTable.value = ''
      await ensureDbExpanded(autoOpen)
      if (myTree !== treeSeq || sid !== props.sessionId) return
      emit('openDatabase', autoOpen, 'objects')
    }
  } catch (e) {`)

rep(`  } finally {
    if (myTree === treeSeq) {
      loading.value = false
      await nextTick()
      recomputeVisible()
    }
  }`,
`  } finally {
    if (myTree === treeSeq) loading.value = false
    if (myTree === treeSeq) {
      await nextTick()
      recomputeVisible()
    }
  }`)

rep(`  treeSeq++
  treeSeq++
`, `  treeSeq++
  autoOpened = false
`)

if (crlf) s = s.replace(/\n/g, '\r\n')
fs.writeFileSync(p, s)
console.log('ok')
