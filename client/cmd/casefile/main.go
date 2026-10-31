package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"nightwatch/casefile/client/casefile"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("casefile", flag.ContinueOnError)
	envPath := flags.String("env", "", "path to a local Casefile environment file")
	baseURL := flags.String("url", "", "Casefile API URL")
	project := flags.String("project", "", "active project slug")
	actor := flags.String("actor", "", "name (provider/model)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, configErr := readEnv(*envPath)
	if configErr != nil {
		return configErr
	}
	if *baseURL == "" {
		*baseURL = first(os.Getenv("CASEFILE_API_URL"), config["CASEFILE_API_URL"], "http://127.0.0.1:8090/api")
	}
	if *project == "" {
		*project = first(os.Getenv("CASEFILE_PROJECT"), config["CASEFILE_PROJECT"])
	}
	if *actor == "" {
		*actor = first(os.Getenv("CASEFILE_ACTOR"), config["CASEFILE_ACTOR"])
	}
	client := casefile.New(*baseURL, *project, *actor)
	rest := flags.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: casefile [--env path] [--project slug] [--actor 'name (provider/model)'] post OPERATION [file|-] | project ensure [slug] [name]")
	}
	var result json.RawMessage
	var err error
	switch rest[0] {
	case "post":
		if len(rest) < 2 || len(rest) > 3 {
			return fmt.Errorf("usage: casefile post OPERATION [file|-]")
		}
		if *project == "" && rest[1] != "projects/list" && rest[1] != "projects/create" && rest[1] != "health" {
			return fmt.Errorf("CASEFILE_PROJECT or --project is required for project-scoped requests")
		}
		var document []byte
		if len(rest) == 3 && rest[2] != "-" {
			document, err = os.ReadFile(rest[2])
		} else {
			document, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		}
		if err != nil {
			return err
		}
		result, err = client.Post(context.Background(), rest[1], document)
	case "project":
		if len(rest) < 2 || rest[1] != "ensure" || len(rest) > 4 {
			return fmt.Errorf("usage: casefile project ensure [slug] [name]")
		}
		slug, name := *project, ""
		if len(rest) > 2 {
			slug = rest[2]
		}
		if len(rest) > 3 {
			name = rest[3]
		}
		result, err = client.EnsureProject(context.Background(), slug, name)
	default:
		return fmt.Errorf("unknown command %q", rest[0])
	}
	if err != nil {
		return err
	}
	var pretty []byte
	pretty, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, string(pretty))
	return err
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func readEnv(explicit string) (map[string]string, error) {
	paths := []string{explicit}
	if explicit == "" {
		paths = []string{"../harness/.env", "../../harness/.env", "harness/.env"}
	}
	result := map[string]string{}
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			if explicit != "" {
				return nil, fmt.Errorf("read Casefile env file %s: %w", path, err)
			}
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			result[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
		break
	}
	return result, nil
}
