package engine

import (
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

type tsConfig struct {
	Extends string `json:"extends"`
	Options struct {
		BaseURL string              `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
	base       string
	pathsDir   string
	baseURLDir string
}

var jsoncTokens = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|//[^\r\n]*|/\*[\s\S]*?\*/`)
var jsoncCommas = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|,[\t\r\n ]*[]}]`)

func readTSConfigs(fsys fs.FS, paths map[string]bool) map[string]tsConfig {
	configs := map[string]tsConfig{}
	for file := range paths {
		name := path.Base(file)
		if name != "tsconfig.json" && !(strings.HasPrefix(name, "tsconfig.") && strings.HasSuffix(name, ".json")) {
			continue
		}
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			continue
		}
		clean := jsoncTokens.ReplaceAllFunc(data, func(token []byte) []byte {
			if token[0] == '/' {
				return []byte(" ")
			}
			return token
		})
		clean = jsoncCommas.ReplaceAllFunc(clean, func(token []byte) []byte {
			if token[0] == ',' {
				return token[1:]
			}
			return token
		})
		var config tsConfig
		if json.Unmarshal(clean, &config) == nil {
			configs[file] = config
		}
	}
	effective := map[string]tsConfig{}
	for file := range configs {
		if path.Base(file) == "tsconfig.json" {
			effective[file] = effectiveTSConfig(file, configs, map[string]bool{})
		}
	}
	return effective
}

func effectiveTSConfig(file string, configs map[string]tsConfig, seen map[string]bool) tsConfig {
	config, ok := configs[file]
	if !ok || seen[file] {
		return tsConfig{}
	}
	seen[file] = true
	parent := tsConfig{}
	if strings.HasPrefix(config.Extends, "./") || strings.HasPrefix(config.Extends, "../") {
		name := path.Join(path.Dir(file), config.Extends)
		if path.Ext(name) != ".json" {
			name += ".json"
		}
		parent = effectiveTSConfig(name, configs, seen)
	}
	config.pathsDir = path.Dir(file)
	if config.Options.Paths == nil {
		config.Options.Paths, config.pathsDir = parent.Options.Paths, parent.pathsDir
	}
	config.baseURLDir = parent.baseURLDir
	if config.Options.BaseURL != "" && !path.IsAbs(config.Options.BaseURL) {
		config.baseURLDir = path.Join(path.Dir(file), config.Options.BaseURL)
	}
	config.base = config.pathsDir
	if config.baseURLDir != "" {
		config.base = config.baseURLDir
	}
	return config
}

func aliasBases(file, specifier string, configs map[string]tsConfig) []string {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if config, ok := configs[path.Join(dir, "tsconfig.json")]; ok {
			if targets, exact := config.Options.Paths[specifier]; exact {
				return aliasTargets(config.base, targets, "")
			}
			patterns := make([]string, 0, len(config.Options.Paths))
			for pattern := range config.Options.Paths {
				if strings.Count(pattern, "*") == 1 {
					patterns = append(patterns, pattern)
				}
			}
			slices.Sort(patterns)
			best, value, length := "", "", -1
			for _, pattern := range patterns {
				prefix, suffix, _ := strings.Cut(pattern, "*")
				if len(prefix) > length && len(specifier) >= len(prefix)+len(suffix) &&
					strings.HasPrefix(specifier, prefix) && strings.HasSuffix(specifier, suffix) {
					best, length = pattern, len(prefix)
					value = specifier[len(prefix) : len(specifier)-len(suffix)]
				}
			}
			if best != "" {
				return aliasTargets(config.base, config.Options.Paths[best], value)
			}
			return nil
		}
		if dir == "." {
			return nil
		}
	}
}

func aliasTargets(base string, targets []string, value string) []string {
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		if !path.IsAbs(target) {
			result = append(result, path.Join(base, strings.ReplaceAll(target, "*", value)))
		}
	}
	return result
}
