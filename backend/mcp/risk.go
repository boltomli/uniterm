package mcp

import (
	"path"
	"strings"
)

// ── 规则说明（前后端共用，必须与 frontend/src/services/agent.ts 的 classifyCommand
// 以及对应前端测试的注释保持逐条一致）────────────────────────────────────────
//
// RISK RULES v2 — backend/mcp/risk.go == frontend/src/services/agent.ts
//
//  0. 空命令/缺失命令 → dangerous（默认关闭）。
//  1. 整行包含 ":|:" 或 "|:&"（小写）→ dangerous（fork bomb，先于分割判断）。
//  2. 整行按 `;`、换行、`|`、`&` 分割为若干段；重定向内部的 `&` 不是分隔符
//     （`2>&1`、`>&2`、`&>file` 不切割）。
//  3. 每段（匹配时小写；head 逐 token 归一）：
//     a. 剥 wrapper（始终保留 ≥1 个 token，绝不越界）：环境变量赋值（token 含
//     `=`）、sudo/doas/nohup/time/nice/env/command/exec。wrapper 识别走
//     normalizeHead，所以 `/usr/bin/sudo`、`C:\x\sudo.exe` 同样被识别。每个
//     wrapper 用自己的选项表解析前导选项（带单独参数的选项连同参数一起跳过；
//     `--` 终止选项）。**表中没有的 wrapper 选项 → dangerous（F-02：未知选项
//     可能携带 payload，如 nice/time/env 的隐藏参数）**；
//     `env -S/--split-string` → dangerous（直接执行任意命令串）。
//     仅剩一个赋值 token 时判 read（不执行任何命令）。
//     b. head 归一：去引号、去前导 `\`、`\`→`/`、取 path.Base、小写、去 `.exe`
//     （覆盖 `/bin/rm -rf /`、`\rm -rf`、`command rm -rf`、C:\...\rm.exe）。
//     c. 嵌套 shell 代码分两档（F-02）：
//     - 隐藏执行体 —— 解释器 `-c/-e/--eval`（sh/bash/zsh/dash/ash/ksh/csh/
//     tsh/fish/cmd/powershell/pwsh/python/node/perl/ruby）、`eval …`、
//     `xargs …` —— payload 无法静态判断，**一律 dangerous**；
//     - `$( … )`、反引号、`find -exec/-execdir <cmd>` —— 递归按本规则分类，
//     结果至少 write；内层 dangerous 直接判 dangerous。
//     d. dangerous：`rm` 带递归+强制（-rf/-fr 或 -r 与 -f 分写）、mkfs*、
//     `dd of=/dev/*`、shutdown/reboot/halt/poweroff/init/fdisk/parted/wipefs/
//     shred、`find -delete`、重定向目标位于 /etc /boot /sys /dev /proc/sys
//     之下（/dev/null 豁免）、cp/mv/tee/install/rsync/dd/truncate 的文件
//     参数位于上述危险路径。
//     e. 每个重定向目标单独判定：`/dev/null` 忽略，其余目标至少 write
//     （`echo k >> ~/.ssh/authorized_keys 2>/dev/null` 必须非 read）。
//     f. 只读放行：head 在 READ_HEADS 中，或 head 在 READ_SUBS 中且第二个
//     token 是列出的只读子命令（git status、kubectl get、systemctl
//     status…），且没有其它 write 信号 → read；只读命令带写选项/写参数
//     （F-02：sort -o/--output、date -s/--set、find -fprint/-fprint0/-fprintf/
//     -fls/-ok/-okdir、git --output、hostname 带参数）→ 至少 write；
//     交互式分页器（less/more/man/top）已移出只读表（可执行子命令）；
//     **其它任何命令默认至少 write**。
//  4. 整行 `curl|wget|fetch … | sh|bash|zsh|sudo sh|sudo bash` → dangerous
//     （classifyDownloadPipe，由调用方叠加）。
//
// classifyCommand grades a shell command into one of the three risk classes
// used by the approval policy. It is a heuristic, not a sandbox: the policy
// dialog is the real boundary; classification only decides when to bother the
// user.
func classifyCommand(cmd string) RiskClass {
	if strings.TrimSpace(cmd) == "" {
		// Default closed: a missing/empty command is never assumed harmless.
		return RiskDangerous
	}
	low := strings.ToLower(cmd)
	// Fork bombs are line-level patterns (":(){ :|:& };:"): pipe-splitting
	// shreds them, so check before segmentation.
	if strings.Contains(low, ":|:") || strings.Contains(low, "|:&") {
		return RiskDangerous
	}
	overall := RiskRead
	for _, seg := range splitSegments(cmd) {
		switch classifySegment(seg) {
		case RiskDangerous:
			return RiskDangerous
		case RiskWrite:
			overall = RiskWrite
		}
	}
	return overall
}

// splitSegments cuts a command line on ;, newlines, | and &. An '&' inside a
// redirection ("2>&1", ">&2", "&>file") is part of the redirect syntax and is
// not a separator.
func splitSegments(cmd string) []string {
	var segs []string
	start := 0
	for i := range len(cmd) {
		switch cmd[i] {
		case ';', '\n', '|':
			segs = append(segs, cmd[start:i])
			start = i + 1
		case '&':
			prev := byte(0)
			if i > 0 {
				prev = cmd[i-1]
			}
			next := byte(0)
			if i+1 < len(cmd) {
				next = cmd[i+1]
			}
			if prev == '>' || next == '>' {
				continue // fd duplication or "&>" — not a separator
			}
			segs = append(segs, cmd[start:i])
			start = i + 1
		}
	}
	return append(segs, cmd[start:])
}

// classifySegment grades one pipeline/命令 list segment.
func classifySegment(seg string) RiskClass {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return RiskRead
	}
	words := strings.Fields(seg)
	head, rest, wrapRisk := stripWrappers(words)
	if wrapRisk == RiskDangerous {
		// F-02: an unknown wrapper option or env -S hides the executed
		// payload — default closed.
		return RiskDangerous
	}
	if len(rest) == 0 || isAssignment(head) {
		// A lone assignment ("FOO=1") runs no command.
		return RiskRead
	}
	head = normalizeHead(head)
	joined := strings.ToLower(seg)

	// Fork bombs: a segment that pipes a self-invocation into the background.
	if strings.Contains(joined, ":|:") || strings.Contains(joined, "|:&") {
		return RiskDangerous
	}

	// F-02: hidden execution bodies (interpreter -c/-e, eval, xargs) grade
	// dangerous outright — their payload cannot be judged statically.
	if len(hardNestedSnippets(head, words)) > 0 {
		return RiskDangerous
	}

	// Nested shell code grades at least write; a dangerous inner grade wins.
	risk := RiskRead
	for _, inner := range nestedSnippets(seg, head, words) {
		switch classifyNested(inner) {
		case RiskDangerous:
			return RiskDangerous
		case RiskWrite:
			risk = RiskWrite
		}
	}

	if matchesDangerous(head, words, joined) {
		return RiskDangerous
	}

	// Every redirection target is judged on its own: /dev/null is the only
	// exemption, everything else is at least a write, and writes into
	// dangerous paths escalate to RiskDangerous.
	for _, target := range redirectTargets(seg) {
		if target == "/dev/null" {
			continue
		}
		if isDangerousPath(target) {
			return RiskDangerous
		}
		risk = RiskWrite
	}

	// File arguments of copy/write commands into dangerous paths escalate.
	if isWriteCommand(head) {
		for _, w := range rest {
			if isDangerousPath(w) {
				return RiskDangerous
			}
		}
	}

	// Default closed: only the read allowlist (heads and known read-only
	// subcommands) may grade read; every other command is at least a write.
	if risk == RiskRead {
		switch {
		case !isReadOnly(head, words):
			risk = RiskWrite
		case hasUnsafeReadOption(head, rest):
			// F-02: a "read" command carrying a write option (sort -o,
			// git log --output, find -fprint, date -s …) must not grade read.
			risk = RiskWrite
		case head == "hostname" && hasPositionalArg(rest):
			// F-02: "hostname <name>" sets the hostname.
			risk = RiskWrite
		}
	}
	return risk
}

// isReadOnly reports whether head is on the read allowlist, either as a
// pure read command or through a listed read-only subcommand.
func isReadOnly(head string, words []string) bool {
	if readOnlyHeads[head] {
		return true
	}
	subs := readOnlySubs[head]
	if len(subs) == 0 || len(words) < 2 {
		return false
	}
	return inList(subs, strings.ToLower(words[1]))
}

// readUnsafeOptions lists the options that turn an otherwise read-only head
// into at least a write: the command still looks like an inspection tool but
// now writes a file or mutates state (F-02 / appendix A of the audit doc).
// Matching is on the lowercased option name up to "=".
var readUnsafeOptions = map[string][]string{
	"sort": {"-o", "--output"},
	"date": {"-s", "--set"},
	"find": {"-fprint", "-fprint0", "-fprintf", "-fls", "-ok", "-okdir"},
	"git":  {"--output"},
}

// hasUnsafeReadOption reports whether any option of words (after head) is a
// write-capable option for this read head.
func hasUnsafeReadOption(head string, words []string) bool {
	opts := readUnsafeOptions[head]
	if len(opts) == 0 {
		return false
	}
	for _, w := range words[1:] {
		if !strings.HasPrefix(w, "-") {
			continue
		}
		name := strings.ToLower(w)
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if inList(opts, name) {
			return true
		}
	}
	return false
}

// hasPositionalArg reports whether words (after head) carry a non-option
// operand — e.g. "hostname myhost" sets the name instead of printing it.
func hasPositionalArg(words []string) bool {
	for _, w := range words[1:] {
		if !strings.HasPrefix(w, "-") {
			return true
		}
	}
	return false
}

// isAssignment reports whether the token is an environment assignment
// ("FOO=1"). Tokens carrying a redirection are not assignments.
func isAssignment(word string) bool {
	return strings.Contains(word, "=") && !strings.ContainsAny(word, "<>")
}

// stripWrappers removes env assignments and privileged/nohup wrappers so
// "sudo -u root rm" is judged as "rm". Wrapper detection goes through
// normalizeHead, so "/usr/bin/sudo" and "C:\\x\\sudo.exe" are recognised too.
// Each wrapper consumes its own leading options via its option table. It
// always keeps at least one token, so a bare wrapper word ("env", "sudo")
// can never run past the slice end.
//
// F-02: an option the wrapper's table does not know grades RiskDangerous —
// unknown wrapper options may carry a payload ("nice -n 5 rm -rf …" and
// "env -S 'rm -rf …'" previously fell through to RiskWrite and skipped the
// confirmation dialog). env -S/--split-string always grades dangerous.
func stripWrappers(words []string) (head string, rest []string, wr RiskClass) {
	wr = RiskRead
	rest = words
	for len(rest) > 1 && (isWrapper(normalizeHead(rest[0])) || isAssignment(rest[0])) {
		w := normalizeHead(rest[0])
		rest = rest[1:]
		if isWrapper(w) {
			r, remaining := stripWrapperOptions(w, rest)
			if r > wr {
				wr = r
			}
			rest = remaining
		}
	}
	if len(rest) == 0 {
		return "", nil, wr
	}
	return rest[0], rest, wr
}

// wrapperSpec is one wrapper's option vocabulary: the options it accepts
// and which of those consume the following word. Anything outside the table
// is unknown → dangerous (default closed). Keys are lowercase because
// matching runs on normalizeHead/ToLower output ("-C" and "-c" collapse,
// "-U" and "-u" collapse — both spellings take an argument either way).
type wrapperSpec struct {
	flags   []string // options with no separate argument
	argOpts []string // options consuming the next word as an argument
}

// wrapperSpecs keeps the option vocabulary of every wrapper stripWrappers
// can peel (sudo/doas share a table: doas is sudo-shaped and the previous
// privOptionArgs list was shared as well).
var wrapperSpecs = map[string]wrapperSpec{
	"sudo": {
		flags: []string{
			"-a", "-b", "-e", "-h", "-i", "-k", "-l", "-n", "-s", "-v",
			"--askpass", "--background", "--edit", "--help", "--list",
			"--login", "--non-interactive", "--preserve-env", "--reset-timestamp",
			"--shell", "--stdin", "--validate", "--version",
		},
		argOpts: []string{
			"-c", "-d", "-g", "-p", "-r", "-t", "-T", "-u",
			"--chdir", "--close-from", "--command-timeout", "--group",
			"--host", "--other-user", "--prompt", "--role", "--type", "--user",
		},
	},
	"doas": {
		flags: []string{
			"-a", "-b", "-s", "--help", "--version",
		},
		argOpts: []string{
			"-c", "-d", "-g", "-p", "-r", "-t", "-u",
			"--group", "--prompt", "--role", "--type", "--user",
		},
	},
	"nohup": {flags: []string{"--help", "--version"}},
	"time": {
		flags:   []string{"-a", "-p", "-v", "--append", "--portability", "--verbose", "--help", "--version"},
		argOpts: []string{"-f", "-o", "--format", "--output"},
	},
	"nice": {
		flags:   []string{"--help", "--version"},
		argOpts: []string{"-n", "--adjustment"},
	},
	"env": {
		flags:   []string{"-i", "--debug", "--help", "--ignore-environment", "--version"},
		argOpts: []string{"-c", "-p", "-s", "--chdir", "--path", "--split-string", "--unset", "-u"},
	},
	"command": {flags: []string{"-p", "-v"}},
	"exec":    {flags: []string{"-c", "-i", "-l"}, argOpts: []string{"-a"}},
}

// stripWrapperOptions consumes a wrapper's leading options using its table.
// "--" ends option parsing (the next token is the command) and assignment
// tokens are skipped. An option the table does not know, or env's
// -S/--split-string, grades RiskDangerous (F-02: default closed).
func stripWrapperOptions(wrapper string, words []string) (RiskClass, []string) {
	spec, ok := wrapperSpecs[wrapper]
	if !ok {
		// isWrapper() only admits wrappers listed above; treat a table gap
		// as unknown instead of silently passing options through.
		return RiskDangerous, words
	}
	for len(words) > 1 && (strings.HasPrefix(words[0], "-") || isAssignment(words[0])) {
		w := strings.ToLower(words[0])
		words = words[1:]
		if isAssignment(w) {
			continue
		}
		if w == "--" {
			break // end of options: the remaining words are the command
		}
		name := w
		if i := strings.IndexByte(w, '='); i >= 0 {
			name = w[:i]
		}
		if wrapper == "env" && (name == "-s" || name == "--split-string") {
			// "env -S 'cmd'" executes its argument as a command line.
			return RiskDangerous, words
		}
		if !inList(spec.argOpts, name) && !inList(spec.flags, name) {
			return RiskDangerous, words // unknown wrapper option
		}
		if !strings.Contains(w, "=") && inList(spec.argOpts, name) && len(words) > 1 {
			words = words[1:] // skip the option's separate argument
		}
	}
	return RiskRead, words
}

// normalizeHead reduces a command word to a comparable base name: quotes and
// quoting backslashes dropped, Windows separators folded to '/', path.Base
// applied, lowercased, ".exe" stripped.
func normalizeHead(head string) string {
	h := strings.TrimSpace(head)
	h = strings.Trim(h, `'"`) // quotes are stripped on both ends
	h = strings.TrimLeft(h, `\`)
	h = strings.ReplaceAll(h, `\`, `/`)
	h = path.Base(h)
	h = strings.ToLower(h)
	h = strings.TrimSuffix(h, ".exe")
	return h
}

// classifyNested grades embedded shell code. The mere presence of the
// construct is already a write (it hides the real command from the caller's
// eyes); the inner text is classified recursively so e.g. `echo $(rm -rf /)`
// still lands on dangerous. Hard execution bodies (interpreter -c/-e, eval,
// xargs) are handled by hardNestedSnippets and grade dangerous outright (F-02).
func classifyNested(text string) RiskClass {
	if strings.TrimSpace(text) == "" {
		return RiskWrite
	}
	if r := classifyCommand(text); r > RiskWrite {
		return r
	}
	return RiskWrite
}

// nestedSnippets extracts the recursively judged embedded shell code of one
// segment: command substitutions, backticks and find -exec bodies. These
// grade at least write (classifyNested) because they hide the inner command
// from the head-based rules.
func nestedSnippets(seg, head string, words []string) []string {
	var out []string
	out = append(out, substitutions(seg)...)

	if head == "find" {
		for i, w := range words {
			if (w == "-exec" || w == "-execdir") && i+1 < len(words) {
				out = append(out, strings.Join(words[i+1:], " "))
				break
			}
		}
	}
	return out
}

// hardNestedSnippets returns the hidden *execution bodies* whose payload
// cannot be judged statically: interpreter -c/-e/--eval strings, eval
// arguments and xargs command tails (F-02). Any hit grades dangerous.
func hardNestedSnippets(head string, words []string) []string {
	var out []string
	if flags, ok := evalFlagHeads[head]; ok {
		for i, w := range words[1:] {
			if inList(flags, strings.ToLower(w)) && i+2 <= len(words)-1 {
				out = append(out, strings.Join(words[i+2:], " "))
				break
			}
		}
	}
	if head == "eval" && len(words) > 1 {
		out = append(out, strings.Join(words[1:], " "))
	}
	if head == "xargs" && len(words) > 1 {
		out = append(out, strings.Join(words[1:], " "))
	}
	return out
}

// evalFlagHeads lists interpreters that execute a code string passed through
// an option, with the option spellings accepted for each.
var evalFlagHeads = map[string][]string{
	"sh": {"-c"}, "bash": {"-c"}, "zsh": {"-c"}, "dash": {"-c"},
	"ash": {"-c"}, "ksh": {"-c"}, "csh": {"-c"}, "tcsh": {"-c"},
	"fish": {"-c"}, "cmd": {"/c", "-c"}, "powershell": {"-c", "-command"},
	"pwsh": {"-c", "-command"}, "python": {"-c"}, "python3": {"-c"},
	"node": {"-e", "--eval"}, "perl": {"-e"}, "ruby": {"-e"},
}

// substitutions extracts the bodies of $( … ) and ` … ` spans. An unbalanced
// construct takes the rest of the string, so "echo $(rm -rf /" still judges
// its tail.
func substitutions(seg string) []string {
	var out []string
	for i := 0; i < len(seg); i++ {
		if seg[i] == '$' && i+1 < len(seg) && seg[i+1] == '(' {
			depth := 1
			j := i + 2
			for j < len(seg) && depth > 0 {
				switch seg[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
				j++
			}
			out = append(out, seg[i+2:j])
			i = j - 1
			continue
		}
		if seg[i] == '`' {
			if end := strings.IndexByte(seg[i+1:], '`'); end >= 0 {
				out = append(out, seg[i+1:i+1+end])
				i += end + 1
			} else {
				out = append(out, seg[i+1:])
				i = len(seg)
			}
		}
	}
	return out
}

// redirectTargets returns every file target written by the segment's
// redirections. fd duplications ("2>&1", ">&2") and quoted spans produce no
// targets.
func redirectTargets(seg string) []string {
	var targets []string
	for i := 0; i < len(seg); i++ {
		switch seg[i] {
		case '\'', '"':
			q := seg[i]
			i++
			for i < len(seg) && seg[i] != q {
				i++
			}
		case '>':
			j := i + 1
			for j < len(seg) && seg[j] == '>' {
				j++
			}
			if j < len(seg) && seg[j] == '|' { // ">|" noclobber
				j++
			}
			for j < len(seg) && (seg[j] == ' ' || seg[j] == '\t') {
				j++
			}
			if j < len(seg) && seg[j] == '&' { // "2>&1" / ">&2"
				j++
				for j < len(seg) && seg[j] >= '0' && seg[j] <= '9' {
					j++
				}
				i = j - 1
				continue
			}
			start := j
			for j < len(seg) && !strings.ContainsRune(" \t\n;|&<>()", rune(seg[j])) {
				j++
			}
			if start < j {
				targets = append(targets, strings.ToLower(strings.Trim(seg[start:j], `"'`)))
			}
			i = j - 1
		}
	}
	return targets
}

// isWriteCommand reports whether head is a command whose target argument is
// a file being written.
func isWriteCommand(head string) bool {
	switch head {
	case "cp", "mv", "tee", "install", "rsync", "dd", "truncate":
		return true
	}
	return false
}

var dangerHeads = []string{
	"mkfs", "shutdown", "reboot", "halt", "poweroff", "init", "fdisk",
	"parted", "wipefs", "shred",
}

var dangerousPaths = []string{"/etc/", "/boot/", "/sys/", "/dev/", "/proc/sys/"}

// isDangerousPath reports whether a redirection/file target points into a
// system path that can brick the machine or hijack authentication.
func isDangerousPath(p string) bool {
	p = strings.ToLower(strings.Trim(p, `'"`))
	for _, d := range dangerousPaths {
		if strings.HasPrefix(p, d) || p == strings.TrimSuffix(d, "/") {
			return true
		}
	}
	return false
}

// matchesDangerous checks unconditional danger patterns.
func matchesDangerous(head string, words []string, joined string) bool {
	if head == "rm" && rmRecursiveForce(words, joined) {
		return true
	}
	if strings.HasPrefix(head, "mkfs") {
		return true
	}
	if head == "dd" && strings.Contains(joined, "of=/dev/") {
		return true
	}
	if inList(dangerHeads, head) {
		return true
	}
	// "find / -delete" removes the whole tree without any rm to spot.
	if head == "find" && containsWord(words, "-delete") {
		return true
	}
	return false
}

// rmRecursiveForce reports whether rm combines recursion with force ("-rf",
// "-fr", or the two flags written separately).
func rmRecursiveForce(words []string, joined string) bool {
	if containsAnyFlag(words, "-rf", "-fr") {
		return true
	}
	if containsWord(words, "-r") && containsAnyFlag(words, "-f") {
		return true
	}
	return strings.Contains(joined, "rm -rf")
}

// classifyDownloadPipe detects "curl/wget … | sh" across the full command
// line and upgrades the whole line to dangerous.
func classifyDownloadPipe(cmd string) bool {
	low := strings.ToLower(cmd)
	if !strings.Contains(low, "|") {
		return false
	}
	for _, left := range []string{"curl", "wget", "fetch"} {
		if !strings.Contains(low, left) {
			continue
		}
		for _, right := range []string{"| sh", "|sh", "| bash", "|bash", "| zsh", "|zsh", "| sudo sh", "| sudo bash"} {
			if strings.Contains(low, right) {
				return true
			}
		}
	}
	return false
}

// readOnlyHeads lists commands that only inspect state. Anything not listed
// here (or in readOnlySubs) is graded at least write.
// F-02: interactive pagers (less/more/man/top) were removed — they accept
// shell escapes ("!sh") / spawn child processes. jq stays: every jq option
// only reads files, none writes or execs.
var readOnlyHeads = map[string]bool{
	// POSIX / Unix-like inspection
	"ag": true, "ack": true, "basename": true, "cat": true, "cd": true,
	"cmp": true, "cut": true, "date": true, "df": true, "diff": true,
	"dirname": true, "dig": true, "du": true, "echo": true, "egrep": true,
	"env": true, "file": true, "find": true, "fgrep": true, "free": true,
	"groups": true, "grep": true, "head": true, "history": true, "hostname": true,
	"host": true, "id": true, "journalctl": true, "jq": true,
	"lscpu": true, "lsof": true, "lsblk": true, "ls": true,
	"md5sum": true, "netstat": true, "nproc": true,
	"nslookup": true, "od": true, "ping": true, "printenv": true,
	"printf": true, "ps": true, "pwd": true, "readlink": true, "realpath": true,
	"rg": true, "seq": true, "sha1sum": true, "sha256sum": true,
	"shasum": true, "sleep": true, "sort": true, "ss": true, "stat": true,
	"strings": true, "tail": true, "test": true,
	"traceroute": true, "tree": true, "true": true, "false": true,
	"tty": true, "type": true, "uname": true, "uniq": true, "uptime": true,
	"wc": true, "which": true, "whereis": true, "who": true, "whoami": true,
	"xxd": true, "hexdump": true, "tr": true, "column": true, "jobs": true,
	// Windows / PowerShell inspection
	"dir": true, "get-childitem": true, "get-command": true,
	"get-computerinfo": true, "get-content": true, "get-date": true,
	"get-item": true, "get-process": true, "get-service": true,
	"ipconfig": true, "measure-object": true, "out-string": true,
	"format-list": true, "format-table": true, "select-string": true,
	"systeminfo": true, "tasklist": true, "test-path": true,
	"ver": true, "where-object": true,
}

// readOnlySubs lists read-only subcommands of multi-action tools. Anything
// else (git push, apt install, kubectl delete …) grades at least write.
var readOnlySubs = map[string][]string{
	"git":     {"status", "log", "diff", "show", "blame", "grep", "rev-parse", "describe", "ls-files", "shortlog", "reflog", "version", "help"},
	"kubectl": {"get", "describe", "logs", "top", "version", "explain", "api-resources", "api-versions", "cluster-info"},
	"docker":  {"ps", "images", "logs", "inspect", "version", "info", "stats", "top", "port", "history", "diff", "events", "df", "search"},
	"podman":  {"ps", "images", "logs", "inspect", "version", "info", "stats", "top", "port", "history", "diff", "events", "df", "search"},
	"systemctl": {"status", "is-active", "is-enabled", "is-failed", "list-units",
		"list-unit-files", "list-timers", "list-sockets", "show", "cat", "get", "help"},
	"service": {"status"},
	"apt":     {"list", "search", "show", "policy", "check", "depends"},
	"apt-get": {"list", "search", "show", "policy", "check", "depends"},
	"dnf":     {"list", "info", "search", "provides", "repolist", "check-update"},
	"yum":     {"list", "info", "search", "provides", "repolist", "check-update"},
	"pip":     {"list", "show", "freeze", "check", "debug"},
	"pip3":    {"list", "show", "freeze", "check", "debug"},
	"npm":     {"ls", "list", "view", "info", "outdated", "search", "why", "ping", "audit", "root", "prefix"},
	"yarn":    {"ls", "list", "view", "info", "outdated", "search", "why", "ping", "audit"},
	"pnpm":    {"ls", "list", "view", "info", "outdated", "search", "why", "ping", "audit"},
	"brew":    {"list", "search", "info", "outdated", "deps", "config", "doctor"},
	"cargo":   {"search", "info", "tree", "metadata", "verify-project"},
	"gem":     {"list", "query", "search", "info", "dependency"},
	"go":      {"version", "env", "list", "doc", "help"},
}

// containsAnyFlag reports whether any word equals one of the flags (exact
// token match, so "-rf" matches but "-r -f" does not — the latter is also
// dangerous but rare; substring matching below catches combined flags).
func containsAnyFlag(words []string, flags ...string) bool {
	for _, w := range words {
		lw := strings.ToLower(w)
		for _, f := range flags {
			if lw == f || strings.Contains(lw, f) {
				return true
			}
		}
	}
	return false
}

func containsWord(words []string, w string) bool {
	for _, word := range words {
		if strings.ToLower(word) == w {
			return true
		}
	}
	return false
}

func inList(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func isWrapper(head string) bool {
	switch strings.ToLower(head) {
	case "sudo", "doas", "nohup", "time", "nice", "env", "command", "exec":
		return true
	}
	return false
}
