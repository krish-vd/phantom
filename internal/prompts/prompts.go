// Package prompts holds the probe set: realistic coding requests where
// models are tempted to invent a convenient package.
package prompts

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

//go:embed prompts.txt
var builtin string

type Prompt struct {
	ID   int    `json:"id"`
	Lang string `json:"lang"` // py or js
	Task string `json:"task"`
}

// Text is what gets sent to the model.
func (p Prompt) Text() string {
	lang := "Python"
	if p.Lang == "js" {
		lang = "JavaScript/TypeScript (Node.js or browser)"
	}
	return fmt.Sprintf("%s. Use %s and the best libraries for the job.", p.Task, lang)
}

// Load reads prompts from path, or the built-in set if path is empty.
func Load(path string) ([]Prompt, error) {
	src := builtin
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		src = string(b)
	}
	var out []Prompt
	for i, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lang, task, ok := strings.Cut(line, ":")
		lang = strings.TrimSpace(lang)
		if !ok || (lang != "py" && lang != "js") {
			return nil, fmt.Errorf("line %d: want \"py: task\" or \"js: task\"", i+1)
		}
		out = append(out, Prompt{ID: len(out) + 1, Lang: lang, Task: strings.TrimSpace(task)})
	}
	return out, nil
}
