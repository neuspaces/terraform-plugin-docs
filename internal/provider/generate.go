package provider

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/hashicorp/terraform-exec/tfinstall"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/mitchellh/cli"
)

var (
	examplesResourceFileTemplate   = resourceFileTemplate("resources/{{.Name}}/resource.tf")
	examplesResourceImportTemplate = resourceFileTemplate("resources/{{.Name}}/import.sh")
	examplesDataSourceFileTemplate = resourceFileTemplate("data-sources/{{ .Name }}/data-source.tf")
	examplesProviderFileTemplate   = providerFileTemplate("provider/provider.tf")

	websiteResourceFileTemplate         = resourceFileTemplate("resources/{{ .ShortName }}.md.tmpl")
	websiteResourceFallbackFileTemplate = resourceFileTemplate("resources.md.tmpl")
	websiteResourceFileStatic           = []resourceFileTemplate{
		resourceFileTemplate("resources/{{ .ShortName }}.md"),
		// TODO: warn for all of these, as they won't render? massage them to the proper output file name?
		resourceFileTemplate("resources/{{ .ShortName }}.markdown"),
		resourceFileTemplate("resources/{{ .ShortName }}.html.markdown"),
		resourceFileTemplate("resources/{{ .ShortName }}.html.md"),
		resourceFileTemplate("r/{{ .ShortName }}.markdown"),
		resourceFileTemplate("r/{{ .ShortName }}.md"),
		resourceFileTemplate("r/{{ .ShortName }}.html.markdown"),
		resourceFileTemplate("r/{{ .ShortName }}.html.md"),
	}
	websiteDataSourceFileTemplate         = resourceFileTemplate("data-sources/{{ .ShortName }}.md.tmpl")
	websiteDataSourceFallbackFileTemplate = resourceFileTemplate("data-sources.md.tmpl")
	websiteDataSourceFileStatic           = []resourceFileTemplate{
		resourceFileTemplate("data-sources/{{ .ShortName }}.md"),
		// TODO: warn for all of these, as they won't render? massage them to the proper output file name?
		resourceFileTemplate("data-sources/{{ .ShortName }}.markdown"),
		resourceFileTemplate("data-sources/{{ .ShortName }}.html.markdown"),
		resourceFileTemplate("data-sources/{{ .ShortName }}.html.md"),
		resourceFileTemplate("d/{{ .ShortName }}.markdown"),
		resourceFileTemplate("d/{{ .ShortName }}.md"),
		resourceFileTemplate("d/{{ .ShortName }}.html.markdown"),
		resourceFileTemplate("d/{{ .ShortName }}.html.md"),
	}
	websiteProviderFileTemplate = providerFileTemplate("index.md.tmpl")
	websiteProviderFileStatic   = []providerFileTemplate{
		providerFileTemplate("index.markdown"),
		providerFileTemplate("index.md"),
		providerFileTemplate("index.html.markdown"),
		providerFileTemplate("index.html.md"),
	}
)

type generator struct {
	legacySidebar bool

	providerName         string
	renderedProviderName string
	renderedWebsiteDir   string
	examplesDir          string
	websiteTmpDir        string
	websiteSourceDir     string

	ui cli.Ui
}

func (g *generator) infof(format string, a ...interface{}) {
	g.ui.Info(fmt.Sprintf(format, a...))
}

func (g *generator) warnf(format string, a ...interface{}) {
	g.ui.Warn(fmt.Sprintf(format, a...))
}

func Generate(ui cli.Ui, legacySidebar bool, providerName, renderedProviderName, renderedWebsiteDir, examplesDir, websiteTmpDir, websiteSourceDir string) error {
	g := &generator{
		legacySidebar: legacySidebar,

		providerName:         providerName,
		renderedProviderName: renderedProviderName,
		renderedWebsiteDir:   renderedWebsiteDir,
		examplesDir:          examplesDir,
		websiteTmpDir:        websiteTmpDir,
		websiteSourceDir:     websiteSourceDir,

		ui: ui,
	}

	ctx := context.Background()

	return g.Generate(ctx)
}

func (g *generator) Generate(ctx context.Context) error {
	var err error

	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	providerName := g.providerName
	if g.providerName == "" {
		providerName = filepath.Base(wd)
	}

	if g.renderedProviderName == "" {
		g.renderedProviderName = providerName
	}

	g.infof("rendering website for provider %q (as %q)", providerName, g.renderedProviderName)

	switch {
	case g.websiteTmpDir == "":
		g.websiteTmpDir, err = ioutil.TempDir("", "tfws")
		if err != nil {
			return err
		}
		defer os.RemoveAll(g.websiteTmpDir)
	default:
		g.infof("cleaning tmp dir %q", g.websiteTmpDir)
		err = os.RemoveAll(g.websiteTmpDir)
		if err != nil {
			return err
		}

		g.infof("creating tmp dir %q", g.websiteTmpDir)
		err = os.MkdirAll(g.websiteTmpDir, 0755)
		if err != nil {
			return err
		}
	}

	websiteSourceDirInfo, err := os.Stat(g.websiteSourceDir)
	switch {
	case os.IsNotExist(err):
		// do nothing, no template dir
	case err != nil:
		return err
	default:
		if !websiteSourceDirInfo.IsDir() {
			return fmt.Errorf("template path is not a directory: %s", g.websiteSourceDir)
		}

		g.infof("copying any existing content to tmp dir")
		err = cp(g.websiteSourceDir, filepath.Join(g.websiteTmpDir, "templates"))
		if err != nil {
			return err
		}
	}

	g.infof("exporting schema from Terraform")
	providerSchema, err := g.terraformProviderSchema(ctx, providerName)
	if err != nil {
		return err
	}

	g.infof("rendering missing docs")
	err = g.renderMissingDocs(g.renderedProviderName, providerSchema)
	if err != nil {
		return err
	}

	g.infof("rendering static website")
	err = g.renderStaticWebsite(g.renderedProviderName, providerSchema)
	if err != nil {
		return err
	}

	// TODO: may not ever need this, unsure on when this will go live
	if g.legacySidebar {
		g.infof("rendering legacy sidebar...")
		g.warnf("TODO...!")
	}

	return nil
}

func (g *generator) renderMissingResourceDoc(providerName, name, typeName string, schema *tfjson.Schema, websiteFileTemplate resourceFileTemplate, fallbackWebsiteFileTemplate resourceFileTemplate, websiteStaticCandidateTemplates []resourceFileTemplate, examplesFileTemplate resourceFileTemplate, examplesImportTemplate *resourceFileTemplate) error {
	tmplPath, err := websiteFileTemplate.Render(name, providerName)
	if err != nil {
		return fmt.Errorf("unable to render path for resource %q: %w", name, err)
	}
	tmplPath = filepath.Join(g.websiteTmpDir, g.websiteSourceDir, tmplPath)
	if fileExists(tmplPath) {
		g.infof("resource %q template exists, skipping", name)
		return nil
	}

	for _, candidate := range websiteStaticCandidateTemplates {
		candidatePath, err := candidate.Render(name, providerName)
		if err != nil {
			return fmt.Errorf("unable to render path for resource %q: %w", name, err)
		}
		candidatePath = filepath.Join(g.websiteTmpDir, g.websiteSourceDir, candidatePath)
		if fileExists(candidatePath) {
			g.infof("resource %q static file exists, skipping", name)
			return nil
		}
	}

	examplePath, err := examplesFileTemplate.Render(name, providerName)
	if err != nil {
		return fmt.Errorf("unable to render example file path for %q: %w", name, err)
	}
	if examplePath != "" {
		examplePath = filepath.Join(g.examplesDir, examplePath)
	}
	if !fileExists(examplePath) {
		examplePath = ""
	}

	importPath := ""
	if examplesImportTemplate != nil {
		importPath, err = examplesImportTemplate.Render(name, providerName)
		if err != nil {
			return fmt.Errorf("unable to render example import file path for %q: %w", name, err)
		}
		if importPath != "" {
			importPath = filepath.Join(g.examplesDir, importPath)
		}
		if !fileExists(importPath) {
			importPath = ""
		}
	}

	targetResourceTemplate := defaultResourceTemplate

	fallbackTmplPath, err := fallbackWebsiteFileTemplate.Render(name, providerName)
	if err != nil {
		return fmt.Errorf("unable to render path for resource %q: %w", name, err)
	}
	fallbackTmplPath = filepath.Join(g.websiteTmpDir, g.websiteSourceDir, fallbackTmplPath)
	if fileExists(fallbackTmplPath) {
		g.infof("resource %q fallback template exists", name)
		tmplData, err := ioutil.ReadFile(fallbackTmplPath)
		if err != nil {
			return fmt.Errorf("unable to read file %q: %w", fallbackTmplPath, err)
		}
		targetResourceTemplate = resourceTemplate(tmplData)
	}

	g.infof("generating template for %q", name)
	md, err := targetResourceTemplate.Render(name, providerName, typeName, examplePath, importPath, schema)
	if err != nil {
		return fmt.Errorf("unable to render template for %q: %w", name, err)
	}

	err = writeFile(tmplPath, md)
	if err != nil {
		return fmt.Errorf("unable to write file %q: %w", tmplPath, err)
	}

	return nil
}

func (g *generator) renderMissingProviderDoc(providerName string, schema *tfjson.Schema, websiteFileTemplate providerFileTemplate, websiteStaticCandidateTemplates []providerFileTemplate, examplesFileTemplate providerFileTemplate) error {
	tmplPath, err := websiteFileTemplate.Render(providerName)
	if err != nil {
		return fmt.Errorf("unable to render path for provider %q: %w", providerName, err)
	}
	tmplPath = filepath.Join(g.websiteTmpDir, g.websiteSourceDir, tmplPath)
	if fileExists(tmplPath) {
		g.infof("provider %q template exists, skipping", providerName)
		return nil
	}

	for _, candidate := range websiteStaticCandidateTemplates {
		candidatePath, err := candidate.Render(providerName)
		if err != nil {
			return fmt.Errorf("unable to render path for provider %q: %w", providerName, err)
		}
		candidatePath = filepath.Join(g.websiteTmpDir, g.websiteSourceDir, candidatePath)
		if fileExists(candidatePath) {
			g.infof("provider %q static file exists, skipping", providerName)
			return nil
		}
	}

	examplePath, err := examplesFileTemplate.Render(providerName)
	if err != nil {
		return fmt.Errorf("unable to render example file path for %q: %w", providerName, err)
	}
	if examplePath != "" {
		examplePath = filepath.Join(g.examplesDir, examplePath)
	}
	if !fileExists(examplePath) {
		examplePath = ""
	}

	g.infof("generating template for %q", providerName)
	md, err := defaultProviderTemplate.Render(providerName, examplePath, schema)
	if err != nil {
		return fmt.Errorf("unable to render template for %q: %w", providerName, err)
	}

	err = writeFile(tmplPath, md)
	if err != nil {
		return fmt.Errorf("unable to write file %q: %w", tmplPath, err)
	}

	return nil
}

func (g *generator) renderMissingDocs(providerName string, providerSchema *tfjson.ProviderSchema) error {
	g.infof("generating missing resource content")
	for name, schema := range providerSchema.ResourceSchemas {
		err := g.renderMissingResourceDoc(providerName, name, "Resource", schema,
			websiteResourceFileTemplate,
			websiteResourceFallbackFileTemplate,
			websiteResourceFileStatic,
			examplesResourceFileTemplate,
			&examplesResourceImportTemplate)
		if err != nil {
			return fmt.Errorf("unable to render doc %q: %w", name, err)
		}
	}

	g.infof("generating missing data source content")
	for name, schema := range providerSchema.DataSourceSchemas {
		err := g.renderMissingResourceDoc(providerName, name, "Data Source", schema,
			websiteDataSourceFileTemplate,
			websiteDataSourceFallbackFileTemplate,
			websiteDataSourceFileStatic,
			examplesDataSourceFileTemplate,
			nil)
		if err != nil {
			return fmt.Errorf("unable to render doc %q: %w", name, err)
		}
	}

	g.infof("generating missing provider content")
	err := g.renderMissingProviderDoc(providerName, providerSchema.ConfigSchema,
		websiteProviderFileTemplate,
		websiteProviderFileStatic,
		examplesProviderFileTemplate,
	)
	if err != nil {
		return fmt.Errorf("unable to render provider doc: %w", err)
	}

	return nil
}

func (g *generator) renderStaticWebsite(providerName string, providerSchema *tfjson.ProviderSchema) error {
	g.infof("cleaning rendered website dir")
	err := os.RemoveAll(g.renderedWebsiteDir)
	if err != nil {
		return err
	}

	shortName := providerShortName(providerName)

	g.infof("rendering templated website to static markdown")

	err = filepath.Walk(g.websiteTmpDir, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			// skip directories
			return nil
		}

		rel, err := filepath.Rel(filepath.Join(g.websiteTmpDir, g.websiteSourceDir), path)
		if err != nil {
			return err
		}

		relDir, relFile := filepath.Split(rel)
		relDir = filepath.ToSlash(relDir)

		// skip special top-level generic resource and data source templates
		if relDir == "" && (relFile == "resources.md.tmpl" || relFile == "data-sources.md.tmpl") {
			return nil
		}

		renderedPath := filepath.Join(g.renderedWebsiteDir, rel)
		err = os.MkdirAll(filepath.Dir(renderedPath), 0755)
		if err != nil {
			return err
		}

		ext := filepath.Ext(path)
		if ext != ".tmpl" {
			g.infof("copying non-template file: %q", rel)
			return cp(path, renderedPath)
		}

		renderedPath = strings.TrimSuffix(renderedPath, ext)

		tmplData, err := ioutil.ReadFile(path)
		if err != nil {
			return fmt.Errorf("unable to read file %q: %w", rel, err)
		}

		out, err := os.Create(renderedPath)
		if err != nil {
			return err
		}
		defer out.Close()

		g.infof("rendering %q", rel)
		switch relDir {
		case "data-sources/":
			resName := shortName + "_" + removeAllExt(relFile)
			resSchema, ok := providerSchema.DataSourceSchemas[resName]
			if ok {
				tmpl := resourceTemplate(tmplData)
				render, err := tmpl.Render("Data Source", providerName, resName, "", "", resSchema)
				if err != nil {
					return fmt.Errorf("unable to render data source template %q: %w", rel, err)
				}
				_, err = out.WriteString(render)
				if err != nil {
					return fmt.Errorf("unable to write rendered string: %w", err)
				}
				return nil
			}
		case "resources/":
			resName := shortName + "_" + removeAllExt(relFile)
			resSchema, ok := providerSchema.ResourceSchemas[resName]
			if ok {
				tmpl := resourceTemplate(tmplData)
				render, err := tmpl.Render("Resource", providerName, resName, "", "", resSchema)
				if err != nil {
					return fmt.Errorf("unable to render resource template %q: %w", rel, err)
				}
				_, err = out.WriteString(render)
				if err != nil {
					return fmt.Errorf("unable to write regindered string: %w", err)
				}
				return nil
			}
		case "": // provider
			if relFile == "index.md.tmpl" {
				tmpl := providerTemplate(tmplData)
				render, err := tmpl.Render(providerName, "", providerSchema.ConfigSchema)
				if err != nil {
					return fmt.Errorf("unable to render provider template %q: %w", rel, err)
				}
				_, err = out.WriteString(render)
				if err != nil {
					return fmt.Errorf("unable to write rendered string: %w", err)
				}
				return nil
			}
		}

		tmpl := docTemplate(tmplData)
		err = tmpl.Render(out)
		if err != nil {
			return fmt.Errorf("unable to render template %q: %w", rel, err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (g *generator) terraformProviderSchema(ctx context.Context, providerName string) (*tfjson.ProviderSchema, error) {
	var err error

	shortName := providerShortName(providerName)

	tmpDir, err := ioutil.TempDir("", "tfws")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	// tmpDir := "/tmp/tftmp"
	// os.RemoveAll(tmpDir)
	// os.MkdirAll(tmpDir, 0755)
	// fmt.Printf("[DEBUG] tmpdir %q\n", tmpDir)

	g.infof("compiling provider %q", shortName)
	providerPath := fmt.Sprintf("plugins/registry.terraform.io/hashicorp/%s/0.0.1/%s_%s", shortName, runtime.GOOS, runtime.GOARCH)
	outFile := filepath.Join(tmpDir, providerPath, fmt.Sprintf("terraform-provider-%s", shortName))
	switch runtime.GOOS {
	case "windows":
		outFile = outFile + ".exe"
	}
	buildCmd := exec.Command("go", "build", "-o", outFile)
	// TODO: constrain env here to make it a little safer?
	_, err = runCmd(buildCmd)
	if err != nil {
		return nil, err
	}

	err = writeFile(filepath.Join(tmpDir, "provider.tf"), fmt.Sprintf(`
provider %[1]q {
}
`, shortName))
	if err != nil {
		return nil, err
	}

	g.infof("getting Terraform binary")
	tfBin, err := tfinstall.Find(ctx, tfinstall.ExactVersion("1.0.5", tmpDir))
	if err != nil {
		return nil, err
	}

	tf, err := tfexec.NewTerraform(tmpDir, tfBin)
	if err != nil {
		return nil, err
	}

	g.infof("running terraform init")
	err = tf.Init(ctx, tfexec.Get(false), tfexec.PluginDir("./plugins"))
	if err != nil {
		return nil, err
	}

	g.infof("getting provider schema")
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		return nil, err
	}

	if ps, ok := schemas.Schemas[shortName]; ok {
		return ps, nil
	}

	if ps, ok := schemas.Schemas["registry.terraform.io/hashicorp/"+shortName]; ok {
		return ps, nil
	}

	return nil, fmt.Errorf("unable to find schema in JSON for provider %q", shortName)
}
