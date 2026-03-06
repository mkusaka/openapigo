package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mkusaka/openapigo/internal/generate"
)

const usage = `Usage: openapigo <command> [options]

Commands:
  generate    Generate Go client code from an OpenAPI spec
  version     Print the generator version

Run 'openapigo <command> -h' for help on a specific command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "generate":
		runGenerate(os.Args[2:])
	case "version":
		fmt.Println("openapigo 0.1.0")
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", os.Args[1], usage)
		os.Exit(1)
	}
}

func runGenerate(args []string) {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	var cfg generate.Config
	var formatMappingRaw string
	var resolveHeaderRaw string
	fs.StringVar(&cfg.Input, "i", "", "path to OpenAPI spec file")
	fs.StringVar(&cfg.Output, "o", "", "output directory for generated code")
	fs.StringVar(&cfg.Package, "package", "", "Go package name (default: directory name)")
	fs.BoolVar(&cfg.SkipValidation, "skip-validation", false, "skip Validate() method generation")
	fs.BoolVar(&cfg.NoReadWriteTypes, "no-read-write-types", false, "skip Request/Response variant type generation")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print file names and sizes without writing")
	fs.StringVar(&formatMappingRaw, "format-mapping", "", "custom format→type mapping (comma-separated, e.g. uuid=github.com/google/uuid.UUID)")
	fs.BoolVar(&cfg.StrictEnums, "strict-enums", false, "generate validation for non-string enums")
	fs.BoolVar(&cfg.ValidateOnUnmarshal, "validate-on-unmarshal", false, "generate UnmarshalJSON that calls Validate()")
	fs.BoolVar(&cfg.Resolve, "resolve", false, "resolve external $ref (file and URL)")
	fs.BoolVar(&cfg.AllowHTTP, "allow-http", false, "allow http:// URLs for remote $ref (requires --resolve)")
	fs.StringVar(&resolveHeaderRaw, "resolve-header", "", "custom headers for remote $ref fetches (comma-separated key:value, e.g. Authorization:Bearer token)")
	fs.DurationVar(&cfg.ResolveTimeout, "resolve-timeout", 0, "timeout for remote $ref fetches (e.g. 30s, 1m)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: openapigo generate -i <spec> -o <output> [-package <name>]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if cfg.Input == "" || cfg.Output == "" {
		fs.Usage()
		os.Exit(1)
	}

	if cfg.Package == "" {
		cfg.Package = generate.SanitizePackageName(inferPackage(cfg.Output))
	}

	if formatMappingRaw != "" {
		cfg.FormatMapping = parseFormatMapping(formatMappingRaw)
	}

	if resolveHeaderRaw != "" {
		cfg.ResolveHeaders = parseHeaders(resolveHeaderRaw)
	}

	if err := generate.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// parseFormatMapping parses "format=import/path.Type,format2=import/path2.Type2".
func parseFormatMapping(raw string) map[string]string {
	m := make(map[string]string)
	for pair := range strings.SplitSeq(raw, ",") {
		pair = strings.TrimSpace(pair)
		if idx := strings.IndexByte(pair, '='); idx > 0 {
			m[pair[:idx]] = pair[idx+1:]
		}
	}
	return m
}

// parseHeaders parses "Key:Value,Key2:Value2".
func parseHeaders(raw string) map[string]string {
	m := make(map[string]string)
	for pair := range strings.SplitSeq(raw, ",") {
		pair = strings.TrimSpace(pair)
		if idx := strings.IndexByte(pair, ':'); idx > 0 {
			m[strings.TrimSpace(pair[:idx])] = strings.TrimSpace(pair[idx+1:])
		}
	}
	return m
}

func inferPackage(dir string) string {
	base := dir
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '/' {
			return base[i+1:]
		}
	}
	return base
}
