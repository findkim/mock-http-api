package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io/ioutil"
	"os"
	"sort"
	"strings"
	"text/template"

	mockapi "github.com/mkeeler/mock-http-api"
)

const (
	tplBuildTags = `
{{- define "build-tags" -}}
{{- range . -}}
// +build {{ . }}
{{ end -}}
{{- if . }}
{{/* just here for the newline */ -}}
{{- end -}}
{{- end -}}
`

	tplHeader = `
{{ define "header" -}}
// Code generated by "mock-api-gen {{.}}"; DO NOT EDIT.
{{- end }}
`

	tplPackage = `
{{ define "package" -}}
package {{ . }}
{{- end }}
`

	tplImports = `
{{- define "imports" -}}
import (
	 "fmt"
	 mockapi "github.com/mkeeler/mock-http-api"

	 {{ range . -}}
	 {{ . }}
	 {{ end }}
)
{{- end -}}
`

	tplPathParameters = `
{{- define "path-parameters" -}}
{{- range . -}}{{ . }} string,{{- end -}}
{{- end -}}
`

	tplReply = `
{{- define "reply" -}}
{{- if eq .ResponseFormat "json" -}}
	{{- if .ResponseType -}}
status int, reply {{ .ResponseType }}
	{{- else -}}
status int, reply interface{}
  {{- end -}}
{{- else if eq .ResponseFormat "string" -}}
status int, reply string
{{- else if eq .ResponseFormat "stream" -}}
status int, reply io.Reader
{{- else if eq .ResponseFormat "func" -}}
reply mockapi.MockResponse
{{- else -}}
status int
{{- end -}}
{{- end -}}
`

	tplQueryParams = `
{{- define "query-params" -}}	
{{- if . -}}
queryParams map[string]string,
{{- end -}}
{{- end -}}
`

	tplRequestHeaders = `
{{- define "request-headers" -}}
{{- if . -}}
headers map[string]string,
{{- end -}}
{{- end -}}	
`

	tplBody = `
{{- define "body" -}}
{{- if eq .BodyFormat "json" -}}
	{{- if .BodyType -}}
body {{ .BodyType }},
	{{- else -}}
body map[string]interface{},
	{{- end -}}
{{- else if or (eq .BodyFormat "string") (eq .BodyFormat "stream") -}}
body []byte,
{{- end -}}
{{- end -}}
`

	tplMockType = `
{{- define "mock-type" -}}
type {{.}} struct {
   *mockapi.MockAPI
}

func New{{.}}(t mockapi.TestingT) *{{.}} {
	return &{{.}}{
		MockAPI: mockapi.NewMockAPI(t),
	}
}
{{- end -}}
`

	tplFunc = `
{{- define "endpoint-func-body" -}}
   req := mockapi.NewMockRequest("{{.Spec.Method}}", 
   {{- if .Spec.PathParameters -}}
   fmt.Sprintf("{{.Spec.Path}}", {{range $index, $param := .Spec.PathParameters }}{{ if $index }},{{ end }}{{ $param }}{{ end }})
   {{- else -}}
   "{{.Spec.Path}}"
   {{- end -}}
   )
   {{- if and (ne .Spec.BodyFormat "none") (ne .Spec.BodyFormat "") -}}
      .WithBody(body)
   {{- end -}}
   {{- if .Spec.QueryParams -}}
      .WithQueryParams(queryParams)
   {{- end -}}
   {{- if .Spec.Headers -}}
      .WithHeaders(headers)
   {{- end }}
   {{ if eq .Spec.ResponseFormat "json" }}
   return m.WithJSONReply(req, status, reply)
   {{- else if eq .Spec.ResponseFormat "string" }}
   return m.WithTextReply(req, status, reply)
   {{- else if eq .Spec.ResponseFormat "stream" }}
   return m.WithStreamingReply(req, status, reply)
   {{- else if eq .Spec.ResponseFormat "func" }}
   return m.WithRequest(req, reply)
   {{- else if or (eq .Spec.ResponseFormat "none") (eq .Spec.ResponseFormat "") }}
   return m.WithNoResponseBody(req, status)
   {{- end}}
{{- end -}}
`

	tplFile = `
{{- template "build-tags" .BuildTags -}}
{{ template "header" .CLIArgs }}

{{ template "package" .Package }}

{{ template "imports" .Imports }}

{{ $receiver := .Receiver }}
{{ template "mock-type" $receiver }}
{{ range .Endpoints }}

func (m *{{ $receiver }}) {{.Name}}(
	{{- template "path-parameters" .Spec.PathParameters -}}
	{{- template "request-headers" .Spec.Headers -}}
	{{- template "query-params" .Spec.QueryParams -}}
	{{- template "body" .Spec }}
	{{- template "reply" .Spec }}) *mockapi.MockAPICall {
{{ template "endpoint-func-body" . }}
}
{{- end -}}
`
)

type inputData struct {
	Imports   map[string]string           `json:"imports"`
	Endpoints map[string]mockapi.Endpoint `json:"endpoints"`
}

type tplEndpoint struct {
	Name string
	Spec mockapi.Endpoint
}

type tplArgs struct {
	CLIArgs   string
	Package   string
	BuildTags []string
	Receiver  string
	Imports   []string
	Endpoints []tplEndpoint
}

func parseTemplate() *template.Template {
	tpl := template.New("mock-api-helpers")

	template.Must(tpl.Parse(tplFile))
	template.Must(tpl.Parse(tplMockType))
	template.Must(tpl.Parse(tplFunc))
	template.Must(tpl.Parse(tplBody))
	template.Must(tpl.Parse(tplRequestHeaders))
	template.Must(tpl.Parse(tplQueryParams))
	template.Must(tpl.Parse(tplPathParameters))
	template.Must(tpl.Parse(tplReply))
	template.Must(tpl.Parse(tplImports))
	template.Must(tpl.Parse(tplPackage))
	template.Must(tpl.Parse(tplHeader))
	template.Must(tpl.Parse(tplBuildTags))

	return tpl
}

// Usage is a replacement usage function for the flags package.
func Usage() {
	fmt.Fprintf(os.Stderr, "Usage of mock-api-gen:\n")
	fmt.Fprintf(os.Stderr, "\tmock-api-gen [flags] -type <type name> -endpoints <var name> [package]\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	flag.PrintDefaults()
}

type config struct {
	input    string
	receiver string
	output   string
	pkgName  string
	tags     []string
}

type stringSliceValue []string

func (v *stringSliceValue) String() string {
	return strings.Join(*v, ",")
}

func (v *stringSliceValue) Set(s string) error {
	*v = append(*v, s)
	return nil
}

func newStringSliceValue(p *[]string) *stringSliceValue {
	return (*stringSliceValue)(p)
}

func parseCLIFlags() config {
	cfg := config{}

	flag.StringVar(&cfg.output, "output", "", "Output file name.")
	flag.StringVar(&cfg.input, "endpoints", "endpoints", "File holding the endpoint configuration.")
	flag.StringVar(&cfg.receiver, "type", "", "Method receiver type the mock API helpers should be generated for")
	flag.StringVar(&cfg.pkgName, "pkg", "", "Name of the package to generate methods in")
	flag.Var(newStringSliceValue(&cfg.tags), "tag", "Build tags the generated file should have. This may be specified multiple times.")

	flag.Usage = Usage
	flag.Parse()

	if cfg.input == "" {
		fmt.Fprintf(os.Stderr, "-endpoints is a required option\n\n")
		flag.Usage()
		os.Exit(1)
	}

	if cfg.receiver == "" {
		fmt.Fprintf(os.Stderr, "-type is a required option\n\n")
		flag.Usage()
		os.Exit(1)
	}

	if cfg.pkgName == "" {
		fmt.Fprintf(os.Stderr, "-pkg is a required option\n\n")
		flag.Usage()
		os.Exit(1)
	}

	if cfg.output == "" {
		fmt.Fprintf(os.Stderr, "-output is a required option\n\n")
		flag.Usage()
		os.Exit(1)
	}

	return cfg
}

func main() {
	cfg := parseCLIFlags()

	var input inputData

	data, err := ioutil.ReadFile(cfg.input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load data from input file %q: %v\n", cfg.input, err)
		os.Exit(1)
	}

	err = json.Unmarshal(data, &input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load JSON from input data file %q: %v\n", cfg.input, err)
		os.Exit(1)
	}

	args := tplArgs{
		CLIArgs:   strings.Join(os.Args[1:], " "),
		BuildTags: cfg.tags,
		Package:   cfg.pkgName,
		Receiver:  cfg.receiver,
	}

	for name, spec := range input.Endpoints {
		args.Endpoints = append(args.Endpoints, tplEndpoint{
			Name: name,
			Spec: spec,
		})
	}

	// ensure they come out in order
	sort.Slice(args.Endpoints, func(i, j int) bool {
		return args.Endpoints[i].Name < args.Endpoints[j].Name
	})

	for pkgName, path := range input.Imports {
		var importPath string
		if strings.HasSuffix(path, "/"+pkgName) {
			importPath = fmt.Sprintf(`"%s"`, path)
		} else {
			importPath = fmt.Sprintf(`%s "%s"`, pkgName, path)
		}
		args.Imports = append(args.Imports, importPath)
	}
	sort.Strings(args.Imports)

	tpl := parseTemplate()

	fmt.Printf("Generating mock endpoints for %s\n", cfg.input)
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, args); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to render template: %v\n", err)
		os.Exit(1)
	}

	src := buf.Bytes()
	formatted, err := format.Source(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to format rendered source: %v\n", err)
		os.Exit(1)
	}

	if err := ioutil.WriteFile(cfg.output, formatted, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write generated source to file %s: %v\n", cfg.output, err)
		os.Exit(1)
	}
	fmt.Printf("Successfully generated source in %s\n", cfg.output)
}
