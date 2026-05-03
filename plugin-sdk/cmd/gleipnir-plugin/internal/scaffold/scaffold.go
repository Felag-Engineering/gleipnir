// Package scaffold generates new plugin project trees from embedded templates.
package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed all:templates
var templateFS embed.FS

// validName matches plugin names: lowercase letters, digits, and hyphens; must
// start with a letter.
var validName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Opts carries the options for scaffold generation.
type Opts struct {
	// Name is the plugin name (lowercase, hyphens allowed, must start with
	// a letter).
	Name string

	// Kind is one of "tool", "channel", "trigger", "combo".
	Kind string

	// Dir is the target directory. It must not already exist.
	Dir string

	// Module is the Go module path to use in the generated go.mod.
	// Defaults to "example.com/<name>" when empty.
	Module string

	// SDKReplace is an optional filesystem path to the plugin-sdk module. When
	// set, a `replace` directive pointing to this path is added to go.mod.
	// Useful for local development before the SDK is published to a registry.
	SDKReplace string
}

// Generate creates the plugin scaffold in opts.Dir. It returns an error if
// the directory already exists or if any template fails to render.
func Generate(opts Opts) error {
	if err := validateName(opts.Name); err != nil {
		return err
	}
	if err := validateKind(opts.Kind); err != nil {
		return err
	}
	if opts.Module == "" {
		opts.Module = "example.com/" + opts.Name
	}

	// Refuse to generate into an existing directory to avoid clobbering work.
	if _, err := os.Stat(opts.Dir); err == nil {
		return fmt.Errorf("directory already exists: %s", opts.Dir)
	}

	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", opts.Dir, err)
	}

	data := templateData{
		Name:       opts.Name,
		Kind:       opts.Kind,
		Module:     opts.Module,
		SDKReplace: opts.SDKReplace,
	}

	// Render common templates.
	if err := renderDir("templates/common", opts.Dir, data); err != nil {
		return err
	}

	// Render kind-specific templates.
	if err := renderDir("templates/"+opts.Kind, opts.Dir, data); err != nil {
		return err
	}

	return nil
}

// templateData is the context passed to every template.
type templateData struct {
	Name       string
	Kind       string
	Module     string
	SDKReplace string
}

// renderDir renders all .tmpl files from the given embedded directory into
// outDir, stripping the .tmpl suffix from output file names. The "gitignore"
// template name is mapped to ".gitignore".
func renderDir(srcDir, outDir string, data templateData) error {
	entries, err := fs.ReadDir(templateFS, srcDir)
	if err != nil {
		return fmt.Errorf("read template dir %s: %w", srcDir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		tmplPath := srcDir + "/" + e.Name()
		outName := templateFileName(e.Name())
		outPath := filepath.Join(outDir, outName)

		if err := renderFile(tmplPath, outPath, data); err != nil {
			return err
		}
	}
	return nil
}

// renderFile renders a single template file to the output path.
func renderFile(tmplPath, outPath string, data templateData) error {
	src, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return fmt.Errorf("read template %s: %w", tmplPath, err)
	}

	tmpl, err := template.New(filepath.Base(tmplPath)).Parse(string(src))
	if err != nil {
		return fmt.Errorf("parse template %s: %w", tmplPath, err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create output file %s: %w", outPath, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("render template %s: %w", tmplPath, err)
	}
	return nil
}

// templateFileName converts a template file name to the output file name.
// Strips the ".tmpl" suffix. "gitignore.tmpl" → ".gitignore".
func templateFileName(name string) string {
	name = strings.TrimSuffix(name, ".tmpl")
	if name == "gitignore" {
		return ".gitignore"
	}
	return name
}

// validateName returns an error if name is not a valid plugin name.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("plugin name must not be empty")
	}
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid plugin name %q: must be lowercase letters, digits, and hyphens; must start with a letter", name)
	}
	return nil
}

// validateKind returns an error if kind is not one of the supported values.
func validateKind(kind string) error {
	switch kind {
	case "tool", "channel", "trigger", "combo":
		return nil
	default:
		return fmt.Errorf("unknown kind %q: must be one of tool, channel, trigger, combo", kind)
	}
}
