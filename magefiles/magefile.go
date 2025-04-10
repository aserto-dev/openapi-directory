//go:build mage
// +build mage

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unicode"
	"unicode/utf8"

	"github.com/aserto-dev/mage-loot/buf"
	"github.com/aserto-dev/mage-loot/common"
	"github.com/aserto-dev/mage-loot/deps"
	"github.com/aserto-dev/mage-loot/fsutil"
	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

var (
	bufDirectory = "buf.build/aserto-dev/directory"
	bufAccess    = "buf.build/authzen/access"
)

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
	tag, err := buf.GetLatestTag(bufDirectory)
	if err != nil {
		fmt.Println("Could not retrieve tags, using latest")
	} else {
		bufDirectory = fmt.Sprintf("%s:%s", bufDirectory, tag.Name)
	}

	return gen(tag.Name, bufDirectory, bufAccess)
}

// Generates from a dev build.
func GenerateDev() error {
	bufImage := filepath.Join(getProtoRepo(), "bin", "directory.bin#format=bin")

	currentVersion, err := common.Version()
	if err != nil {
		return err
	}

	return gen(currentVersion, bufImage, bufAccess)
}

func gen(version string, bufImage ...string) error {
	for _, image := range bufImage {
		if err := buf.Run(
			buf.AddArg("generate"),
			buf.AddArg("--template"),
			buf.AddArg(filepath.Join("buf", "buf.gen.yaml")),
			buf.AddArg(image),
		); err != nil {
			return err
		}
	}

	if err := mergeOpenAPI(); err != nil {
		return err
	}

	fmt.Println("Publishing OpenAPI:", version)
	return publishOpenAPIv3(version)
}

func getProtoRepo() string {
	protoRepo := os.Getenv("PROTO_REPO")
	if protoRepo == "" {
		protoRepo = "../pb-directory"
	}
	return protoRepo
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
				"aserto/directory/common/v3/common.swagger.json",
				"aserto/directory/model/v3/model.swagger.json",
				"aserto/directory/reader/v3/reader.swagger.json",
				"aserto/directory/writer/v3/writer.swagger.json",
				"aserto/directory/importer/v3/importer.swagger.json",
				"aserto/directory/exporter/v3/exporter.swagger.json",
				"aserto/directory/assertion/v3/assertion.swagger.json",
				"aserto/directory/openapi/v3/openapi.swagger.json",
				"access/v1/access.swagger.json",
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

type Service struct {
	input      string
	config     string
	extensions []string
	jsonOutput string
	yamlOutput string
}

// publish OpenAPI v3 specification file.
func publishOpenAPIv3(version string) error {
	services := []Service{
		{
			input:  "service/directory/openapi.json",
			config: "config/directory-config.json",
			extensions: []string{
				"aserto/directory/model/v3/model.ext.openapi.json",
			},
			jsonOutput: "publish/directory/openapi.json",
			yamlOutput: "publish/directory/openapi.yaml",
		},
	}

	for _, service := range services {
		if err := publishOpenAPIv3Service(service, version); err != nil {
			return errors.Wrapf(err, "failed to publish service from [%s]", service.input)
		}
	}

	return nil
}

func publishOpenAPIv3Service(service Service, version string) error {
	switch {
	case !fileExists(service.input):
		return errors.Errorf("input file not found (%s)\n", service.input)
	case !fileExists(service.config):
		return errors.Errorf("config file not found (%s)\n", service.config)
	}

	var doc2 openapi2.T
	if err := loadOpenAPI(service.input, &doc2); err != nil {
		return errors.Wrapf(err, "load openapi v2 [%s]", service.input)
	}

	spec3, err := openapi2conv.ToV3(&doc2)
	if err != nil {
		return errors.Wrapf(err, "convert input OpenAPI to v3")
	}

	if err := applyConfigToSpec(spec3, service.config); err != nil {
		return errors.Wrapf(err, "apply config [%s] to spec", service.config)
	}

	spec3.Info.Version = version

	populateAsertoSecuritySchemes(spec3.Components.SecuritySchemes)

	for _, ext := range service.extensions {
		if err := applyExtensionToSpec(spec3, ext); err != nil {
			return errors.Wrapf(err, "apply extension [%s] to spec", ext)
		}
	}

	stripPathsWithTag(spec3.Paths, "INTERNAL_API")

	if err := writeOpenAPI(spec3, service.jsonOutput); err != nil {
		return errors.Wrapf(err, "write json output file [%s]", service.jsonOutput)
	}

	if err := writeOpenAPI(spec3, service.yamlOutput); err != nil {
		return errors.Wrapf(err, "write yaml output file [%s]", service.yamlOutput)
	}

	return nil
}

type Decoder interface {
	Decode(v any) error
}

type Encoder interface {
	Encode(v any) error
}

func loadOpenAPI[T any](path string, doc T) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.Wrapf(err, "open openapi v3 file [%s]", path)
	}
	defer f.Close()

	ext := filepath.Ext(path)

	var decoder Decoder
	switch ext {
	case ".json":
		decoder = json.NewDecoder(f)
	case ".yaml", ".yml":
		decoder = yaml.NewDecoder(f)
	default:
		return fmt.Errorf("unsupported file extension '%s' in [%s]", ext, path)
	}

	return decoder.Decode(&doc)
}

func writeOpenAPI(spec *openapi3.T, path string) error {
	f, err := createFileAndDir(path)
	if err != nil {
		return errors.Wrapf(err, "create openapi v3 file [%s]", path)
	}
	defer f.Close()

	ext := filepath.Ext(path)

	var encoder Encoder
	switch ext {
	case ".json":
		jsonEncoder := json.NewEncoder(f)
		jsonEncoder.SetIndent("", "  ")
		encoder = jsonEncoder
	case ".yaml", ".yml":
		yamlEncoder := yaml.NewEncoder(f)
		yamlEncoder.SetIndent(2)
		encoder = yamlEncoder
	default:
		return fmt.Errorf("unsupported file extension '%s' in [%s]", ext, path)
	}

	if err := encoder.Encode(spec); err != nil {
		return errors.Wrapf(err, "encode openapi v3 file [%s]", path)
	}

	return nil
}

func applyConfigToSpec(spec *openapi3.T, configPath string) error {
	var cfg openapi3.T
	if err := loadOpenAPI(configPath, &cfg); err != nil {
		return errors.Wrapf(err, "load config [%s]", configPath)
	}

	spec.Info = cfg.Info
	spec.Servers = cfg.Servers
	spec.ExternalDocs = cfg.ExternalDocs

	return nil
}

func populateAsertoSecuritySchemes(schemes openapi3.SecuritySchemes) {
	if tenantSec, ok := schemes["TenantID"]; ok {
		tenantSec.Value = &openapi3.SecurityScheme{
			Type: "apiKey",
			Name: "aserto-tenant-id",
			In:   "header",
		}
		tenantSec.Value.Description = "Aserto Tenant ID"
	}

	if jwtSec, ok := schemes["JWT"]; ok {
		jwtSec.Value = openapi3.NewJWTSecurityScheme()
		jwtSec.Value.Description = "Aserto JWT token"
	}
}

func applyExtensionToSpec(spec *openapi3.T, extPath string) error {
	var ext openapi3.T
	if err := loadOpenAPI(extPath, &ext); err != nil {
		return errors.Wrapf(err, "load extension [%s]", extPath)
	}

	if ext.Info == nil {
		ext.Info = spec.Info
	}

	for path, def := range ext.Paths.Map() {
		item := spec.Paths.Value(path)

		if item == nil {
			// The path doesn't exist in the spec, so just add it.
			spec.Paths.Set(path, def)
			continue
		}

		// The path exists in the spec, so merge the operations.
		for opKey, op := range def.Operations() {
			if _, ok := item.Operations()[opKey]; ok {
				return errors.Errorf("operation [%s] already exists in path [%s]", opKey, path)
			}

			item.SetOperation(opKey, op)
		}
	}

	return nil
}

func stripPathsWithTag(paths *openapi3.Paths, tag string) {
	for path, def := range paths.Map() {
		for opKey, op := range def.Operations() {
			if contains(op.Tags, tag) {
				def.SetOperation(opKey, nil)
			}
		}
		if len(def.Operations()) == 0 {
			paths.Delete(path)
		}
	}
}

func createFileAndDir(path string) (*os.File, error) {
	fsutil.EnsureDir(filepath.Dir(path))
	return os.Create(path)
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
	if err := os.RemoveAll("service"); err != nil {
		return err
	}

	if err := cleanFile("publish/directory/openapi.json"); err != nil {
		return err
	}

	return cleanFile("publish/directory/openapi.yaml")
}

func cleanFile(path string) error {
	if err := os.Remove(path); err != nil {
		if _, ok := err.(*os.PathError); !ok {
			return err
		}
	}

	return nil
}
