//go:build mage
// +build mage

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"github.com/aserto-dev/mage-loot/buf"
	"github.com/aserto-dev/mage-loot/common"
	"github.com/aserto-dev/mage-loot/deps"
	"github.com/aserto-dev/mage-loot/fsutil"
	"github.com/aserto-dev/mage-loot/mage"
	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

var bufImage = "buf.build/aserto-dev/directory"

func All() error {
	Deps()

	err := Clean()
	if err != nil {
		return err
	}

	err = Generate()
	if err != nil {
		return err
	}

	return nil
}

// install required dependencies.
func Deps() {
	deps.GetAllDeps()
}

// Generate the code
func Generate() error {

	tag, err := buf.GetLatestTag(bufImage)
	if err != nil {
		fmt.Println("Could not retrieve tags, using latest")
	} else {
		bufImage = fmt.Sprintf("%s:%s", bufImage, tag.Name)
	}

	return gen(bufImage, bufImage, tag.Name)
}

func gen(bufImage, fileSources, version string) error {
	files, err := getClientFiles(fileSources)
	if err != nil {
		return err
	}

	pathSeparator := string(os.PathListSeparator)
	path := os.Getenv("PATH") +
		pathSeparator +
		filepath.Dir(deps.GoBinPath("protoc-gen-openapiv2"))

	err = buf.RunWithEnv(map[string]string{
		"PATH": path,
	},
		buf.AddArg("generate"),
		buf.AddArg("--template"),
		buf.AddArg(filepath.Join("buf", "buf.gen.yaml")),
		buf.AddArg(bufImage),
		buf.AddPaths(files),
	)
	if err != nil {
		return err
	}

	err = mergeOpenAPI()
	if err != nil {
		return err
	}

	err = publishOpenAPIv3(version)
	if err != nil {
		return err
	}

	err = generateEmbedCode()
	if err != nil {
		return err
	}

	return nil
}

func getClientFiles(fileSources string) ([]string, error) {
	var clientFiles []string

	bufExportDir, err := os.MkdirTemp("", "bufimage")
	if err != nil {
		return clientFiles, err
	}
	bufExportDir = filepath.Join(bufExportDir, "")

	defer os.RemoveAll(bufExportDir)
	err = buf.Run(
		buf.AddArg("export"),
		buf.AddArg(fileSources),
		buf.AddArg("--exclude-imports"),
		buf.AddArg("-o"),
		buf.AddArg(bufExportDir),
	)
	if err != nil {
		return clientFiles, err
	}
	excludePattern := ""

	protoFiles, err := fsutil.Glob(filepath.Join(bufExportDir, "aserto", "**", "*.proto"), excludePattern)
	if err != nil {
		return clientFiles, err
	}

	for _, protoFile := range protoFiles {
		clientFiles = append(clientFiles, strings.TrimPrefix(protoFile, bufExportDir+string(filepath.Separator)))
	}

	return clientFiles, nil
}

// Generates from a dev build.
func GenerateDev() error {
	// err := BuildDev()
	// if err != nil {
	//     return err
	// }

	bufImage := filepath.Join(getProtoRepo(), "bin", "directory.bin#format=bin")
	fileSources := filepath.Join(getProtoRepo(), "proto#format=dir")

	currentVersion, err := common.Version()
	if err != nil {
		return err
	}

	return gen(bufImage, fileSources, currentVersion)
}

func getProtoRepo() string {
	protoRepo := os.Getenv("PROTO_REPO")
	if protoRepo == "" {
		protoRepo = "../pb-directory"
	}
	return protoRepo
}

// Builds the aserto proto image
func BuildDev() error {
	return mage.RunDirs(path.Join(getProtoRepo(), "magefiles"), getProtoRepo(), mage.AddArg("build"))
}

// join openapi.json specs from subservices into a single openapi.json file for the service.
func mergeOpenAPI() error {
	const (
		repo = "github.com/aserto-dev/openapi-directory"
	)

	type Service struct {
		outfile     string
		subServices []string
	}

	services := []Service{
		{
			outfile: "service/directory/openapi.json",
			subServices: []string{
				"aserto/directory/common/v2/common.swagger.json",
				"aserto/directory/reader/v2/reader.swagger.json",
				"aserto/directory/writer/v2/writer.swagger.json",
			},
		},
	}

	for _, service := range services {
		err := common.MergeOpenAPI(repo, service.outfile, service.subServices)
		if err != nil {
			return err
		}
	}

	return nil
}

// publish OpenAPI v3 specification file.
func publishOpenAPIv3(version string) error {
	type Service struct {
		input      string
		config     string
		jsonOutput string
		yamlOutput string
	}

	services := []Service{
		{
			input:      "service/directory/openapi.json",
			config:     "config/directory-config.json",
			jsonOutput: "publish/directory/openapi.json",
			yamlOutput: "publish/directory/openapi.yaml",
		},
	}

	for _, service := range services {

		switch {
		case !fileExists(service.input):
			return errors.Errorf("input file not found (%s)\n", service.input)
		case !fileExists(service.config):
			return errors.Errorf("config file not found (%s)\n", service.config)
		}

		inputReader, err := os.Open(service.input)
		if err != nil {
			return errors.Wrapf(err, "open input file")
		}

		configReader, err := os.Open(service.config)
		if err != nil {
			return errors.Wrapf(err, "open config file")
		}

		fsutil.EnsureDir(filepath.Dir(service.jsonOutput))
		jsonWriter, err := os.Create(service.jsonOutput)
		if err != nil {
			return errors.Wrapf(err, "create json output file")
		}

		fsutil.EnsureDir(filepath.Dir(service.jsonOutput))

		var doc2 openapi2.T
		dec := json.NewDecoder(inputReader)
		if err := dec.Decode(&doc2); err != nil {
			return errors.Wrapf(err, "decode input file")
		}

		var cfg openapi3.T
		configDecoder := json.NewDecoder(configReader)
		if err := configDecoder.Decode(&cfg); err != nil {
			return errors.Wrapf(err, "decode config file")
		}

		spec3, err := openapi2conv.ToV3(&doc2)
		if err != nil {
			return errors.Wrapf(err, "convert input OpenAPI to v3")
		}

		spec3.Info = cfg.Info
		spec3.Info.Version = version
		spec3.Servers = cfg.Servers
		spec3.ExternalDocs = cfg.ExternalDocs

		const InternalAPI string = "INTERNAL_API"

		for pathKey, path := range spec3.Paths {
			for opKey, op := range spec3.Paths[pathKey].Operations() {
				if contains(op.Tags, InternalAPI) {
					resp := toLowerFirst(strings.Replace(op.OperationID, "_", "", 1)) + "Response"
					fmt.Printf("remove component schema %s\n", resp)
					delete(spec3.Components.Schemas, resp)

					fmt.Printf("remove operation %s %s \n", opKey, pathKey)
					path.SetOperation(opKey, nil)
				}
			}
			if len(spec3.Paths[pathKey].Operations()) == 0 {
				fmt.Printf("remove path %s\n", pathKey)
				delete(spec3.Paths, pathKey)
			}
		}

		if tenantSec, ok := spec3.Components.SecuritySchemes["TenantID"]; ok {
			tenantSec.Value = &openapi3.SecurityScheme{
				Type: "apiKey",
				Name: "aserto-tenant-id",
				In:   "header",
			}
			tenantSec.Value.Description = "Aserto Tenant ID"
		}

		if jwtSec, ok := spec3.Components.SecuritySchemes["JWT"]; ok {
			jwtSec.Value = openapi3.NewJWTSecurityScheme()
			jwtSec.Value.Description = "Aserto JWT token"
		}

		jsonEncoder := json.NewEncoder(jsonWriter)
		jsonEncoder.SetIndent("", "  ")
		if err := jsonEncoder.Encode(&spec3); err != nil {
			return errors.Wrapf(err, "encode json output file")
		}

		yamlWriter, err := os.Create(service.yamlOutput)
		if err != nil {
			return errors.Wrapf(err, "create yaml output file")
		}

		yamlEncoder := yaml.NewEncoder(yamlWriter)
		defer yamlEncoder.Close()
		yamlEncoder.SetIndent(2)
		if err := yamlEncoder.Encode(&spec3); err != nil {
			return errors.Wrapf(err, "encode yaml output file")
		}
	}

	return nil
}

func generateEmbedCode() error {
	t, err := template.New("embed").Parse(embedTemplate)
	if err != nil {
		return err
	}

	targets := []string{
		"publish/directory/openapi.go",
	}
	for _, target := range targets {
		w, err := os.Create(target)
		if err != nil {
			return err
		}

		err = t.Execute(w, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func fileExists(filepath string) bool {
	info, err := os.Stat(filepath)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}
	return false
}

func toLowerFirst(s string) string {
	if s == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}

// Removes generated files
func Clean() error {
	err := os.RemoveAll("service")
	if err != nil {
		return err
	}
	return os.RemoveAll("publish")
}

const embedTemplate string = `package openapi

import "embed"

//go:embed openapi.json
var staticAssets embed.FS

// Static embedded FS service openapi.json file.
func Static() embed.FS {
	return staticAssets
}
`
