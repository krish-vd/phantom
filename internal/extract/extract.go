// Package extract pulls package names out of free-form model output:
// install commands (pip, uv, poetry, npm, yarn, pnpm, bun) and import
// statements inside code blocks.
package extract

import (
	_ "embed"
	"regexp"
	"sort"
	"strings"
)

// Ecosystem is a package registry.
type Ecosystem string

const (
	PyPI Ecosystem = "pypi"
	NPM  Ecosystem = "npm"
)

// Source says how a name was found. Install commands are strong evidence
// that the model believes a package exists; imports are weaker for Python,
// because a module name is not always the distribution name.
type Source string

const (
	FromInstall Source = "install"
	FromImport  Source = "import"
)

// Mention is one package name found in a model response.
type Mention struct {
	Ecosystem Ecosystem `json:"ecosystem"`
	Name      string    `json:"name"`
	Source    Source    `json:"source"`
}

// Key identifies a package independent of how it was found.
func (m Mention) Key() string { return string(m.Ecosystem) + "/" + m.Name }

//go:embed py_stdlib.txt
var pyStdlibRaw string

var pyStdlib = func() map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(pyStdlibRaw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m[l] = true
		}
	}
	// Modules removed from recent Python versions still appear in model output.
	for _, n := range []string{"distutils", "imp", "asynchat", "asyncore", "smtpd",
		"cgi", "cgitb", "crypt", "nntplib", "telnetlib", "audioop", "chunk", "imghdr",
		"mailcap", "msilib", "nis", "ossaudiodev", "pipes", "sndhdr", "spwd", "sunau",
		"uu", "xdrlib", "lib2to3", "__future__", "__main__"} {
		m[n] = true
	}
	return m
}()

// Import names whose PyPI distribution has a different name. Without this,
// `import cv2` would be reported as a missing package called "cv2".
var pyImportToDist = map[string]string{
	"cv2": "opencv-python", "PIL": "pillow", "sklearn": "scikit-learn",
	"yaml": "pyyaml", "bs4": "beautifulsoup4", "dotenv": "python-dotenv",
	"jwt": "pyjwt", "dateutil": "python-dateutil", "magic": "python-magic",
	"Crypto": "pycryptodome", "OpenSSL": "pyopenssl", "serial": "pyserial",
	"usb": "pyusb", "skimage": "scikit-image", "attr": "attrs",
	"docx": "python-docx", "pptx": "python-pptx", "fitz": "pymupdf",
	"telegram": "python-telegram-bot", "slugify": "python-slugify",
	"jose": "python-jose", "multipart": "python-multipart", "git": "gitpython",
	"Levenshtein": "python-levenshtein", "zmq": "pyzmq", "win32api": "pywin32",
	"win32com": "pywin32", "MySQLdb": "mysqlclient", "psycopg2": "psycopg2-binary",
	"google": "", "googleapiclient": "google-api-python-client", "grpc": "grpcio",
	"pkg_resources": "setuptools", "faiss": "faiss-cpu", "gi": "pygobject",
	"socketio": "python-socketio", "engineio": "python-engineio",
	"ruamel": "ruamel.yaml", "sentence_transformers": "sentence-transformers",
	"llama_cpp": "llama-cpp-python", "tree_sitter": "tree-sitter",
	"Bio": "biopython", "nacl": "pynacl", "kafka": "kafka-python",
	"ldap": "python-ldap", "xdist": "pytest-xdist", "vlc": "python-vlc",
	"wx": "wxpython", "OpenGL": "pyopengl", "lxml": "lxml", "_cffi_backend": "",
}

var nodeBuiltins = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`assert async_hooks buffer child_process cluster console
		constants crypto dgram diagnostics_channel dns domain events fs http http2 https
		inspector module net os path perf_hooks process punycode querystring readline repl
		stream string_decoder sys test timers tls trace_events tty url util v8 vm wasi
		worker_threads zlib`) {
		nodeBuiltins[n] = true
	}
}

var (
	fenceRe  = regexp.MustCompile("(?s)```([A-Za-z0-9_+-]*)[^\n]*\n(.*?)```")
	inlineRe = regexp.MustCompile("`([^`\n]+)`")

	pipCmdRe = regexp.MustCompile(`(?m)\b(?:pip3?|python3?(?:\.\d+)?\s+-m\s+pip|uv\s+pip|pipx)\s+install\s+([^\n` + "`" + `;&|#]+)|\b(?:poetry|uv|pipenv|pdm|rye)\s+(?:add|install)\s+([^\n` + "`" + `;&|#]+)`)
	npmCmdRe = regexp.MustCompile(`(?m)\b(?:npm\s+(?:install|i|add)|yarn\s+(?:global\s+)?add|pnpm\s+(?:add|install|i)|bun\s+(?:add|install|i)|npx)\s+([^\n` + "`" + `;&|#]+)`)

	pyImportRe = regexp.MustCompile(`(?m)^\s*import\s+([A-Za-z_][\w.]*(?:\s+as\s+\w+)?(?:\s*,\s*[A-Za-z_][\w.]*(?:\s+as\s+\w+)?)*)\s*(?:#.*)?$`)
	pyFromRe   = regexp.MustCompile(`(?m)^\s*from\s+([A-Za-z_][\w.]*)\s+import\s`)

	jsRequireRe = regexp.MustCompile(`\brequire\(\s*['"]([^'"\s]+)['"]\s*\)`)
	jsImportRe  = regexp.MustCompile(`(?m)\bimport\s+(?:[\w*{}\s,$]+\s+from\s+)?['"]([^'"\s]+)['"]|\bimport\(\s*['"]([^'"\s]+)['"]\s*\)|\bexport\s+[\w*{}\s,$]+\s+from\s+['"]([^'"\s]+)['"]`)

	pyNameRe  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	npmNameRe = regexp.MustCompile(`^(?:@[a-z0-9-~][a-z0-9-._~]*/)?[a-z0-9-~][a-z0-9-._~]*$`)
	pyNormRe  = regexp.MustCompile(`[-_.]+`)
)

// pip flags that consume the next token.
var pipArgFlags = map[string]bool{"-r": true, "--requirement": true, "-c": true,
	"--constraint": true, "-e": true, "--editable": true, "-i": true, "--index-url": true,
	"--extra-index-url": true, "-f": true, "--find-links": true, "-t": true, "--target": true,
	"--prefix": true, "--python": true, "-p": true, "--channel": true, "--group": true,
	"--root": true, "--platform": true, "--only-binary": true, "--no-binary": true}

var npmArgFlags = map[string]bool{"--registry": true, "--prefix": true, "-w": true,
	"--workspace": true, "--filter": true, "-C": true, "--dir": true, "-p": true, "--package": true}

// NormalizePyPI applies PEP 503 normalization.
func NormalizePyPI(name string) string {
	return pyNormRe.ReplaceAllString(strings.ToLower(name), "-")
}

// FromText returns the unique package mentions in a model response.
// Install-command mentions win over import mentions for the same name.
func FromText(text string) []Mention {
	found := map[string]Mention{}
	add := func(m Mention) {
		if m.Name == "" {
			return
		}
		if prev, ok := found[m.Key()]; ok && prev.Source == FromInstall {
			return
		}
		found[m.Key()] = m
	}

	// Install commands are only trusted inside code: fenced blocks and inline
	// `code` spans. Prose like "you can pip install the requests library"
	// would otherwise produce a package called "the".
	var code []string
	for _, b := range fenceRe.FindAllStringSubmatch(text, -1) {
		code = append(code, b[2])
	}
	for _, sp := range inlineRe.FindAllStringSubmatch(fenceRe.ReplaceAllString(text, ""), -1) {
		code = append(code, sp[1])
	}
	shell := strings.Join(code, "\n")

	for _, sm := range pipCmdRe.FindAllStringSubmatch(shell, -1) {
		args := sm[1]
		if args == "" {
			args = sm[2]
		}
		for _, n := range pipArgs(args) {
			add(Mention{PyPI, n, FromInstall})
		}
	}
	for _, sm := range npmCmdRe.FindAllStringSubmatch(shell, -1) {
		isNpx := strings.HasPrefix(strings.TrimSpace(sm[0]), "npx")
		for i, n := range npmArgs(sm[1]) {
			if isNpx && i > 0 {
				break // npx runs one package; the rest are its arguments
			}
			add(Mention{NPM, n, FromInstall})
		}
	}

	for _, block := range fenceRe.FindAllStringSubmatch(text, -1) {
		lang, code := strings.ToLower(block[1]), block[2]
		isPy := lang == "python" || lang == "py" || lang == "python3"
		isJS := lang == "js" || lang == "javascript" || lang == "ts" || lang == "typescript" ||
			lang == "jsx" || lang == "tsx" || lang == "mjs" || lang == "node"
		if lang == "" { // untagged: try both, the patterns don't overlap
			isPy, isJS = true, true
		}
		if isPy {
			for _, n := range pyImports(code) {
				add(Mention{PyPI, n, FromImport})
			}
		}
		if isJS {
			for _, n := range jsImports(code) {
				add(Mention{NPM, n, FromImport})
			}
		}
	}

	out := make([]Mention, 0, len(found))
	for _, m := range found {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func pipArgs(s string) []string {
	var out []string
	toks := strings.Fields(s)
	for i := 0; i < len(toks); i++ {
		t := strings.Trim(toks[i], `"'\`)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "-") {
			if pipArgFlags[t] {
				i++
			}
			continue
		}
		if strings.ContainsAny(t, "/:") || t == "." || strings.HasSuffix(t, ".txt") ||
			strings.HasSuffix(t, ".whl") || strings.HasSuffix(t, ".gz") || strings.HasSuffix(t, ".toml") {
			continue
		}
		// Strip extras and version specifiers: fastapi[all]>=0.110 -> fastapi
		if j := strings.IndexAny(t, "[=<>!~;@ ,"); j >= 0 {
			t = t[:j]
		}
		if pyNameRe.MatchString(t) && !isPlaceholder(t) {
			out = append(out, NormalizePyPI(t))
		}
	}
	return out
}

func npmArgs(s string) []string {
	var out []string
	toks := strings.Fields(s)
	for i := 0; i < len(toks); i++ {
		t := strings.Trim(toks[i], `"'\`)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "-") {
			if npmArgFlags[t] {
				i++
			}
			continue
		}
		if strings.Contains(t, ":") || strings.HasPrefix(t, ".") || strings.HasPrefix(t, "/") ||
			strings.HasSuffix(t, ".tgz") {
			continue
		}
		if n := npmBase(t); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// npmBase turns "react@18", "@types/node@20", "lodash/fp" into the package name.
func npmBase(t string) string {
	t = strings.TrimPrefix(t, "npm:")
	scoped := strings.HasPrefix(t, "@")
	body := t
	if scoped {
		body = t[1:]
	}
	if j := strings.Index(body, "@"); j >= 0 { // version tag
		body = body[:j]
	}
	parts := strings.Split(body, "/")
	var name string
	if scoped {
		if len(parts) < 2 {
			return ""
		}
		name = "@" + parts[0] + "/" + parts[1]
	} else {
		name = parts[0]
	}
	if !npmNameRe.MatchString(name) || nodeBuiltins[name] || isPlaceholder(name) {
		return ""
	}
	return name
}

func pyImports(code string) []string {
	var mods []string
	for _, m := range pyImportRe.FindAllStringSubmatch(code, -1) {
		for _, part := range strings.Split(m[1], ",") {
			f := strings.Fields(part)
			if len(f) > 0 {
				mods = append(mods, f[0])
			}
		}
	}
	for _, m := range pyFromRe.FindAllStringSubmatch(code, -1) {
		mods = append(mods, m[1])
	}
	var out []string
	for _, mod := range mods {
		top := strings.SplitN(mod, ".", 2)[0]
		if top == "" || pyStdlib[top] {
			continue
		}
		if dist, ok := pyImportToDist[top]; ok {
			if dist == "" {
				continue
			}
			top = dist
		}
		if pyNameRe.MatchString(top) && !isPlaceholder(top) {
			out = append(out, NormalizePyPI(top))
		}
	}
	return out
}

func jsImports(code string) []string {
	var specs []string
	for _, m := range jsRequireRe.FindAllStringSubmatch(code, -1) {
		specs = append(specs, m[1])
	}
	for _, m := range jsImportRe.FindAllStringSubmatch(code, -1) {
		for _, s := range m[1:] {
			if s != "" {
				specs = append(specs, s)
			}
		}
	}
	var out []string
	for _, s := range specs {
		if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "node:") ||
			strings.HasPrefix(s, "~") || strings.HasPrefix(s, "#") || strings.Contains(s, ":") {
			continue
		}
		if strings.HasPrefix(s, "@/") { // common path alias, not a package
			continue
		}
		if n := npmBase(s); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// Names models use as stand-ins rather than real suggestions.
func isPlaceholder(n string) bool {
	switch strings.ToLower(n) {
	case "package", "package-name", "package_name", "your-package", "your_package",
		"pkg", "name", "module", "mypackage", "my-package", "my_package", "foo", "bar",
		"example", "your-library", "library", "lib", "app", "myapp", "my-app", "x", "y":
		return true
	}
	return false
}
